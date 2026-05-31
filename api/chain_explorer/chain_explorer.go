// Package chain_explorer serves a browser-based explorer focused on chained
// accounts (sequencers, foundries, delegations, generic chains) in the latest
// reliable branch (LRB). Mounted into the node API server.
//
// First slice: the /list endpoint (max / kind / index_value filters) + a
// minimal static HTML table. See claude/chain_explorer.md for the full spec;
// it is implemented incrementally.
package chain_explorer

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
)

//go:embed chain_explorer.html
var chainExplorerHTML []byte

const (
	defaultMaxRows = 200
	maxRowsCeiling = 2000
)

// Env is what the chain explorer needs from the node API server: the LRB
// state reader (for iteration) and the LRB branch data (for the lrbid +
// total supply header). Satisfied by the API server.
type Env interface {
	LatestReliableState() (multistate.SugaredStateReader, error)
	GetLatestReliableBranch() *multistate.BranchData
}

// Register wires the chain explorer routes (HTML page + JSON list API) into
// the supplied addHandler.
func Register(addHandler func(string, func(http.ResponseWriter, *http.Request)), env Env) {
	addHandler(api.PathChainExplorer, servePage)
	addHandler(api.PathChainExplorerList, func(w http.ResponseWriter, r *http.Request) { serveList(w, r, env) })
	addHandler(api.PathChainExplorerUTXO, func(w http.ResponseWriter, r *http.Request) { serveUTXO(w, r, env) })
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(chainExplorerHTML)
}

// JSON response types

type listResponse struct {
	LRBID         string `json:"lrbid"`      // raw hex txid; UI parses the slot from it
	LRBDashed     string `json:"lrb_dashed"` // full dashed notation, for display
	WallClockUnix int64  `json:"wall_clock_unix"`
	TotalSupply   uint64 `json:"total_supply"`
	// FrozenCoverage is the cumulative total tokens frozen by delegations at
	// the LRB (stem-projected state aggregate). A subset of TotalSupply.
	FrozenCoverage uint64 `json:"frozen_coverage"`
	// SlotDurationMs lets the UI convert a slot delta to wall-clock duration.
	SlotDurationMs int64 `json:"slot_duration_ms"`
	// BranchInflationBase is the per-branch max inflation bonus at the LRB slot;
	// the UI uses it to estimate nominal total inflation per slot.
	BranchInflationBase uint64 `json:"branch_inflation_base"`
	Matched             int    `json:"matched"`
	Returned       int   `json:"returned"`
	Truncated      bool  `json:"truncated"`
	Rows           []row `json:"rows"`
}

type row struct {
	ChainID           string          `json:"chain_id"`
	OutputID          string          `json:"output_id"`
	Kind              string          `json:"kind"`
	Balance           uint64          `json:"balance"`
	Frozen            uint64          `json:"frozen,omitempty"`
	OriginSlot        uint32          `json:"origin_slot"`
	TransitionCounter uint64          `json:"transition_counter"`
	BranchCounter     uint32          `json:"branch_counter,omitempty"`
	LastActiveSlot    uint32          `json:"last_active_slot"`
	IndexValues       []string        `json:"index_values"`
	Sequencer         *sequencerInfo  `json:"sequencer,omitempty"`
	Foundry           *foundryInfo    `json:"foundry,omitempty"`
	Delegation        *delegationInfo `json:"delegation,omitempty"`
}

type sequencerInfo struct {
	Name                     string `json:"name"`
	EpochSlots               uint32 `json:"epoch_slots"`
	MaxFrozenEpochs          byte   `json:"max_frozen_epochs"`
	ProfitMarginPromille     uint16 `json:"profit_margin_promille"`
	MinFee                   uint64 `json:"min_fee"`
	Greedy                   bool   `json:"greedy"`
	CumulativeChainInflation uint64 `json:"cumulative_chain_inflation"`
}

type foundryInfo struct {
	Supply uint64 `json:"supply"`
}

type delegationInfo struct {
	RequiredInflationCutPromille uint16 `json:"required_inflation_cut_promille"`
	MaxFrozenEpochs              byte   `json:"max_frozen_epochs"`
	LastFrozenEpoch              uint32 `json:"last_frozen_epoch,omitempty"`
	// StatusAtLRB describes the revocation status relative to the LRB slot:
	// "safe revocation in <dur>" (frozen), "safe revocation for <dur>"
	// (inside the window), or "not frozen".
	StatusAtLRB string `json:"status_at_lrb"`
}

const (
	kindAll        = "all"
	kindSequencer  = "sequencer"
	kindFoundry    = "foundry"
	kindDelegation = "delegation"
	kindGeneric    = "generic"
)

func serveList(w http.ResponseWriter, r *http.Request, env Env) {
	api.SetHeader(w)

	// --- parse + validate query params
	q := r.URL.Query()
	maxRows := defaultMaxRows
	if s := q.Get("max"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			api.WriteErr(w, "invalid 'max': must be a positive integer")
			return
		}
		maxRows = n
	}
	if maxRows > maxRowsCeiling {
		maxRows = maxRowsCeiling
	}

	kind := q.Get("kind")
	if kind == "" {
		kind = kindAll
	}
	switch kind {
	case kindAll, kindSequencer, kindFoundry, kindDelegation, kindGeneric:
	default:
		api.WriteErr(w, "invalid 'kind': one of all|sequencer|foundry|delegation|generic")
		return
	}

	// index_value: general "any index-values entry" filter (escape hatch).
	var indexValueFilter []byte
	if s := q.Get("index_value"); s != "" {
		b, err := hex.DecodeString(s)
		if err != nil {
			api.WriteErr(w, "invalid 'index_value': must be hex")
			return
		}
		indexValueFilter = b
	}
	// controller: matches index_values[0] — the controller (sig/foundry/generic)
	// or the master (delegation). Lowercased for hex comparison against the row.
	controllerFilter, err := hexParamLower(q.Get("controller"))
	if err != nil {
		api.WriteErr(w, "invalid 'controller': must be hex")
		return
	}
	// delegation_target: matches index_values[1] on delegation rows.
	targetFilter, err := hexParamLower(q.Get("delegation_target"))
	if err != nil {
		api.WriteErr(w, "invalid 'delegation_target': must be hex")
		return
	}

	// --- LRB header (lrbid + total supply)
	br := env.GetLatestReliableBranch()
	if br == nil {
		http.Error(w, "no LRB available (node still syncing)", http.StatusServiceUnavailable)
		return
	}
	lrbTxid := br.TxID()
	lib := ledger.L(base.MaxSlot)
	lrbSlot := br.Slot()

	resp := listResponse{
		LRBID:               lrbTxid.StringHex(),
		LRBDashed:           lrbTxid.String(),
		WallClockUnix:       time.Now().Unix(),
		TotalSupply:         br.Supply,
		FrozenCoverage:      br.FrozenCoverage,
		SlotDurationMs:      ledger.SlotDuration().Milliseconds(),
		BranchInflationBase: lib.BranchInflationBonusBase(lrbSlot),
		Rows:                make([]row, 0, maxRows),
	}

	// process applies the filter predicate to one chain output and accumulates
	// it into the response. Same predicate regardless of how the candidate was
	// found, so the indexed-scan path below enforces identical semantics:
	// controller == index_values[0], delegation target == index_values[1] on a
	// genuine delegate lock (kind == delegation).
	process := func(o *ledger.OutputWithChainID) {
		rw := makeRow(o, lib, lrbSlot)
		if kind != kindAll && rw.Kind != kind {
			return
		}
		if controllerFilter != "" && (len(rw.IndexValues) == 0 || rw.IndexValues[0] != controllerFilter) {
			return
		}
		if targetFilter != "" && (rw.Kind != kindDelegation || len(rw.IndexValues) < 2 || rw.IndexValues[1] != targetFilter) {
			return
		}
		if indexValueFilter != nil && !containsIndexValue(o.Output, indexValueFilter) {
			return
		}
		resp.Matched++
		if len(resp.Rows) < maxRows {
			resp.Rows = append(resp.Rows, rw)
		}
	}

	// indexedScanValue is the raw controllers-partition key to prefix-scan when
	// an indexed filter is set. The controllers partition holds one entry per
	// non-empty index_values element, so scanning it narrows the candidate set
	// from "all chains" to "outputs carrying this value at some position" — a
	// big win at 100k+ chains. The per-row predicate still runs, so a candidate
	// that merely shares the value at the wrong position (or isn't a chain) is
	// dropped. Priority: controller, then delegation target, then generic
	// index_value (a single scan; remaining filters apply in-memory). The
	// unfiltered / kind-only case is left on the full chain walk untouched.
	var indexedScanValue []byte
	switch {
	case controllerFilter != "":
		indexedScanValue, _ = hex.DecodeString(controllerFilter) // already validated
	case targetFilter != "":
		indexedScanValue, _ = hex.DecodeString(targetFilter) // already validated
	case indexValueFilter != nil:
		indexedScanValue = indexValueFilter
	}

	err = util.CatchPanicOrError(func() error {
		rdr, err1 := env.LatestReliableState()
		if err1 != nil {
			return err1
		}
		if indexedScanValue != nil {
			return rdr.IterateOutputsForAccount(indexedScanValue, func(oid base.OutputID, o *ledger.Output) bool {
				if owc, ok := asChainOutput(o, oid); ok {
					process(owc)
				}
				return true
			})
		}
		return rdr.IterateChainedOutputs(func(o ledger.OutputWithChainID) bool {
			process(&o)
			return true
		})
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	// sort the returned page by balance desc (first-slice default; richer
	// sort options come later). matched is the pre-truncation count.
	sort.Slice(resp.Rows, func(i, j int) bool {
		return resp.Rows[i].Balance > resp.Rows[j].Balance
	})
	resp.Returned = len(resp.Rows)
	resp.Truncated = resp.Matched > resp.Returned

	respBin, err := json.MarshalIndent(&resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, _ = w.Write(respBin)
}

// utxoResponse is the decoded UTXO shown in the per-row "utxo" popup.
type utxoResponse struct {
	ChainID        string   `json:"chain_id"`
	OutputID       string   `json:"output_id"`        // raw hex (copied to clipboard on click)
	OutputIDDashed string   `json:"output_id_dashed"` // dashed notation, for display
	SizeBytes      int      `json:"size_bytes"`
	Elements       []string `json:"elements"` // one decoded tuple element per line: amounts, index values, then constraints
	// Chain is the decoded chain constraint, present for every chain.
	Chain *utxoChainData `json:"chain,omitempty"`
	// IsSequencer marks a sequencer output; the UI renders a "Sequencer data"
	// section (N/A when SeqData failed to decode).
	IsSequencer bool         `json:"is_sequencer"`
	SeqData     *utxoSeqData `json:"seq_data,omitempty"`
}

// utxoChainData is the decoded chain constraint, shown with real field names.
type utxoChainData struct {
	OriginSlot               uint32 `json:"origin_slot"`
	CumulativeChainInflation uint64 `json:"cumulative_chain_inflation"`
	CumulativeBranchBonus    uint64 `json:"cumulative_branch_bonus"`
	TransitionCounter        uint64 `json:"transition_counter"`
	BranchCounter            uint32 `json:"branch_counter"`
}

// utxoSeqData is the decoded sequencer metadata shown with real field names.
type utxoSeqData struct {
	Name                 string `json:"name"`
	MinimumFee           uint64 `json:"minimum_fee"`
	InflationCutPromille uint16 `json:"inflation_cut_promille"`
	Pace                 byte   `json:"pace"`
	Greedy               bool   `json:"greedy"`
	IgnoreFreezeBound    bool   `json:"ignore_freeze_bound"`
}

// serveUTXO returns the decoded UTXO of a chain at the LRB. It is fetched by
// chain_id (NOT a previously seen output_id), because the chain's current
// output ID changes on every transition.
func serveUTXO(w http.ResponseWriter, r *http.Request, env Env) {
	api.SetHeader(w)

	chainIDHex := r.URL.Query().Get("chain_id")
	if chainIDHex == "" {
		api.WriteErr(w, "missing 'chain_id'")
		return
	}
	chainID, err := base.ChainIDFromHexString(chainIDHex)
	if err != nil {
		api.WriteErr(w, "invalid 'chain_id': "+err.Error())
		return
	}

	if env.GetLatestReliableBranch() == nil {
		http.Error(w, "no LRB available (node still syncing)", http.StatusServiceUnavailable)
		return
	}

	var resp utxoResponse
	err = util.CatchPanicOrError(func() error {
		rdr, err1 := env.LatestReliableState()
		if err1 != nil {
			return err1
		}
		o, err1 := rdr.GetChainOutputWithID(chainID)
		if err1 != nil {
			return err1
		}
		resp = utxoResponse{
			ChainID:        chainID.StringHex(),
			OutputID:       o.ID.StringHex(),
			OutputIDDashed: o.ID.String(),
			SizeBytes:      len(o.Output.Bytes()),
			Elements:       o.Output.LinesSource().Slice(),
		}
		if cc := o.Output.ChainConstraint(); cc != nil {
			resp.Chain = &utxoChainData{
				OriginSlot:               cc.OriginSlot,
				CumulativeChainInflation: cc.CumulativeChainInflation,
				CumulativeBranchBonus:    cc.CumulativeBranchBonus,
				TransitionCounter:        cc.TransitionCounter,
				BranchCounter:            cc.BranchCounter,
			}
		}
		if o.Output.IsSequencerOutput() {
			resp.IsSequencer = true
			if sd, err2 := ledger.ParseSequencerData(o.Output); err2 == nil {
				resp.SeqData = &utxoSeqData{
					Name:                 sd.Name(),
					MinimumFee:           sd.MinimumFee(),
					InflationCutPromille: sd.InflationProfitMarginPromille(),
					Pace:                 sd.Pace(),
					Greedy:               sd.IsGreedy(),
					IgnoreFreezeBound:    sd.IsIgnoreFreezeBound(),
				}
			}
		}
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	respBin, err := json.MarshalIndent(&resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, _ = w.Write(respBin)
}

// asChainOutput wraps a raw output as OutputWithChainID iff it carries a chain
// constraint (i.e. it is a chain tip). Returns ok=false for non-chain outputs
// that merely share an index value with the scanned filter. Mirrors the chainID
// resolution in IterateChainedOutputs / GetOutputsDelegatedToAccount2.
func asChainOutput(o *ledger.Output, oid base.OutputID) (*ledger.OutputWithChainID, bool) {
	cc := o.ChainConstraint()
	if cc == nil {
		return nil, false
	}
	chainID := cc.ChainID
	if cc.IsOrigin() {
		chainID = base.MakeOriginChainID(oid)
	}
	out := &ledger.OutputWithChainID{
		OutputWithID:        ledger.OutputWithID{ID: oid, Output: o},
		ChainConstraintData: ledger.ChainConstraintData{ChainConstraint: *cc},
	}
	out.ChainID = chainID
	return out, true
}

// makeRow classifies a chained output and builds its table row. Kind
// discriminator (mutually exclusive, in priority order): sequencer
// constraint at index 4, then foundry constraint at index 4, then a
// delegate lock at index 2, else generic.
func makeRow(o *ledger.OutputWithChainID, lib *ledger.Library, lrbSlot uint32) row {
	cc := o.ChainConstraint
	rw := row{
		ChainID:           o.ChainID.StringHex(),
		OutputID:          o.ID.StringHex(),
		Kind:              kindGeneric,
		Balance:           o.Output.TokenBalance(),
		OriginSlot:        cc.OriginSlot,
		TransitionCounter: cc.TransitionCounter,
		BranchCounter:     cc.BranchCounter,
		LastActiveSlot:    o.ID.Slot(),
		IndexValues:       indexValuesHex(o.Output),
	}

	// index 4 is shared by sequencer() and foundry() (mutually exclusive).
	if seqBytes, err := o.Output.ConstraintAt(ledger.SequencerConstraintFixedIndex); err == nil && len(seqBytes) > 0 {
		if sc, err := ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib); err == nil {
			rw.Kind = kindSequencer
			rw.Frozen = uint64(o.Output.FrozenCoverage(0))
			si := &sequencerInfo{
				EpochSlots:               sc.EpochSlots,
				MaxFrozenEpochs:          sc.MaxFrozenEpochs,
				CumulativeChainInflation: cc.CumulativeChainInflation,
			}
			if sd, err := ledger.ParseSequencerData(o.Output); err == nil {
				si.Name = sd.Name()
				si.ProfitMarginPromille = sd.InflationProfitMarginPromille()
				si.MinFee = sd.MinimumFee()
				si.Greedy = sd.IsGreedy()
			}
			rw.Sequencer = si
			return rw
		}
		if f, err := ledger.FoundryFromBytesWithLib(seqBytes, lib); err == nil {
			rw.Kind = kindFoundry
			rw.Foundry = &foundryInfo{Supply: f.Supply}
			return rw
		}
	}

	if dOut, ok := ledger.DelegationOutputFromOutputWithChainIDWithLib(o, lib); ok {
		rw.Kind = kindDelegation
		rw.Delegation = &delegationInfo{
			RequiredInflationCutPromille: dOut.RequiredInflationCut,
			MaxFrozenEpochs:              dOut.MaxFrozenEpochs,
			LastFrozenEpoch:              dOut.LastFrozenEpoch,
			StatusAtLRB:                  delegationStatusAtLRB(&dOut, lrbSlot),
		}
		return rw
	}

	return rw
}

// hexParamLower validates that s is hex (or empty) and returns it lowercased,
// ready for comparison against the row's hex index_values entries.
func hexParamLower(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", err
	}
	return strings.ToLower(s), nil
}

// delegationStatusAtLRB describes the delegation's revocation status relative
// to the LRB slot, derived from the safe-revocation window [from, to]:
//
//	(a) "frozen. Safe revocation in <dur>" — frozen (before the window): <dur> until it opens
//	(b) "safe revocation for <dur>" — inside the window: <dur> remaining
//	(c) "not frozen"                — no applicable window, or past it
func delegationStatusAtLRB(d *ledger.DelegationOutput, lrbSlot uint32) string {
	from, to, applicable := d.SafeRevocationWindow()
	if !applicable {
		return "not frozen"
	}
	slotDur := ledger.SlotDuration()
	switch {
	case lrbSlot < from:
		return "frozen. Safe revocation in " + humanDur(time.Duration(int64(from)-int64(lrbSlot))*slotDur)
	case lrbSlot <= to:
		return "safe revocation for " + humanDur(time.Duration(int64(to)-int64(lrbSlot))*slotDur)
	default:
		return "not frozen"
	}
}

func humanDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Round(time.Second).String()
}

// indexValuesHex returns the raw index-values tuple (constraint index 1) as
// a slice of hex strings, one per entry.
func indexValuesHex(o *ledger.Output) []string {
	bin, err := o.ConstraintAt(ledger.ConstraintIndexIndexValues)
	if err != nil || len(bin) == 0 {
		return nil
	}
	values, err := ledger.IndexValuesFromBytes(bin)
	if err != nil {
		return nil
	}
	ret := make([]string, len(values))
	for i, v := range values {
		ret[i] = hex.EncodeToString(v)
	}
	return ret
}

func containsIndexValue(o *ledger.Output, want []byte) bool {
	bin, err := o.ConstraintAt(ledger.ConstraintIndexIndexValues)
	if err != nil || len(bin) == 0 {
		return false
	}
	values, err := ledger.IndexValuesFromBytes(bin)
	if err != nil {
		return false
	}
	for _, v := range values {
		if bytes.Equal(v, want) {
			return true
		}
	}
	return false
}
