// Package chain_explorer serves a browser-based explorer focused on chained
// accounts (sequencers, foundries, delegations, generic chains) in the latest
// reliable branch (LRB). Mounted into the node API server.
//
// First slice: the /list endpoint (max / kind / index_value filters) + a
// minimal static HTML table. See claude/archive/shipped/chain_explorer.md for the full spec;
// it is implemented incrementally.
package chain_explorer

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	// LatestBranchSlot / BranchDataForSlot back the sequencer view's branch
	// coverage-delta column (cache incl. uncommitted branches, plus DB).
	LatestBranchSlot() uint32
	BranchDataForSlot(slot uint32) []*multistate.BranchData
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
	// CurrentSlot is the ledger slot derived from the server wall clock now.
	// The UI shows it against the LRB slot to indicate the sync situation
	// (how many slots the LRB lags behind the current slot).
	CurrentSlot uint32 `json:"current_slot"`
	TotalSupply uint64 `json:"total_supply"`
	// FrozenCoverage is the cumulative total tokens frozen by delegations at
	// the LRB (stem-projected state aggregate). A subset of TotalSupply.
	FrozenCoverage uint64 `json:"frozen_coverage"`
	// The following are the remaining stem-projected aggregates of the LRB
	// branch (see multistate.BranchData / ledger StemLock+OracleData):
	//   TotalCoverage / CoverageDelta — ledger coverage and its slot delta;
	//   SlotInflation — inflation emitted in the LRB slot;
	//   NumConfirmedTransactions — new txs in the branch past cone;
	//   NumSeqTransactions / NumSeq — new seq txs and distinct active
	//   sequencers in the LRB slot.
	TotalCoverage            uint64 `json:"total_coverage"`
	CoverageDelta            uint64 `json:"coverage_delta"`
	SlotInflation            uint64 `json:"slot_inflation"`
	NumConfirmedTransactions uint32 `json:"num_confirmed_transactions"`
	NumSeqTransactions       uint32 `json:"num_seq_transactions"`
	NumSeq                   uint32 `json:"num_seq"`
	// SlotDurationMs lets the UI convert a slot delta to wall-clock duration.
	SlotDurationMs int64 `json:"slot_duration_ms"`
	// BranchInflationBase is the per-branch max inflation bonus at the LRB slot;
	// the UI uses it to estimate nominal total inflation per slot.
	BranchInflationBase uint64 `json:"branch_inflation_base"`
	Returned            int    `json:"returned"`
	// ScanCapped marks an incomplete result: either the kind-only / unfiltered
	// state traversal was bounded by a scan budget (so matching chains beyond it
	// were not examined), or more rows matched than `max`. No exact total is
	// computed — that would require walking every chain, which is what the cap
	// avoids. The indexed-filter paths (controller/target/index_value) are
	// naturally bounded, so this only trips there if matches exceed `max`.
	ScanCapped bool  `json:"scan_capped"`
	Rows       []row `json:"rows"`
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
	Mine              *mineInfo       `json:"mine,omitempty"`
}

type sequencerInfo struct {
	Name                     string `json:"name"`
	EpochSlots               uint32 `json:"epoch_slots"`
	MaxFrozenEpochs          byte   `json:"max_frozen_epochs"`
	ProfitMarginPromille     uint16 `json:"profit_margin_promille"`
	MinFee                   uint64 `json:"min_fee"`
	Greedy                   bool   `json:"greedy"`
	CumulativeChainInflation uint64 `json:"cumulative_chain_inflation"`
	// CoverageDelta / BranchInflationBonus are this sequencer's branch coverage
	// delta and branch inflation bonus (VRF) taken from its branch in a recent
	// settled slot (the slot before the latest one that has any branch), sourced
	// from the DB cache (incl. uncommitted) plus the DB. Both nil (rendered "-")
	// when the sequencer produced no branch in that slot.
	CoverageDelta        *uint64 `json:"coverage_delta,omitempty"`
	BranchInflationBonus *uint64 `json:"branch_inflation_bonus,omitempty"`
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

// mineInfo describes the fair-launch mine chain. Every transit of that chain is
// exactly one successful mining transaction minting the constant amount A, so
// the chain's transition counter is the number of mined transactions and the
// minted total is that counter times A.
type mineInfo struct {
	MinedTransactions uint64 `json:"mined_transactions"`
	MinedAmount       uint64 `json:"mined_amount"`
	// Remaining is R, the still-mintable motes carried in the mineLock.
	Remaining uint64 `json:"remaining"`
}

const (
	kindAll        = "all"
	kindSequencer  = "sequencer"
	kindFoundry    = "foundry"
	kindDelegation = "delegation"
	kindMine       = "mining"
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
	case kindAll, kindSequencer, kindFoundry, kindDelegation, kindMine, kindGeneric:
	default:
		api.WriteErr(w, "invalid 'kind': one of all|sequencer|foundry|delegation|mining|generic")
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
		LRBID:                    lrbTxid.StringHex(),
		LRBDashed:                lrbTxid.String(),
		WallClockUnix:            time.Now().Unix(),
		CurrentSlot:              ledger.SlotNow(),
		TotalSupply:              br.Supply,
		FrozenCoverage:           br.FrozenCoverage,
		TotalCoverage:            br.TotalCoverage,
		CoverageDelta:            br.CoverageDelta,
		SlotInflation:            br.SlotInflation,
		NumConfirmedTransactions: br.NumConfirmedTransactions,
		NumSeqTransactions:       br.NumSeqTransactions,
		NumSeq:                   br.NumSeq,
		SlotDurationMs:           ledger.SlotDuration().Milliseconds(),
		BranchInflationBase:      lib.BranchInflationBonusBase(lrbSlot),
		Rows:                     make([]row, 0, maxRows),
	}

	// For the sequencer view, resolve each sequencer's real branch coverage delta
	// and branch inflation bonus from a recent settled slot: the slot before the
	// latest one that carries any branch (so all competing branches are present).
	// Sourced from the DB cache (incl. uncommitted branches) plus the DB. A
	// sequencer without a branch in that slot renders "-".
	var branchBySeq map[base.ChainID]*multistate.BranchData
	if kind == kindSequencer {
		if latest := env.LatestBranchSlot(); latest > 1 {
			branchBySeq = make(map[base.ChainID]*multistate.BranchData)
			for _, bd := range env.BranchDataForSlot(latest - 1) {
				// forks can yield more than one branch per sequencer; keep the heaviest
				if ex, ok := branchBySeq[bd.SequencerID]; !ok || bd.CoverageDelta > ex.CoverageDelta {
					branchBySeq[bd.SequencerID] = bd
				}
			}
		}
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
		if rw.Sequencer != nil {
			if bd := branchBySeq[o.ChainID]; bd != nil {
				cd := bd.CoverageDelta
				rw.Sequencer.CoverageDelta = &cd
				if bd.SequencerOutput != nil {
					bonus := bd.SequencerOutput.Output.Inflation()
					rw.Sequencer.BranchInflationBonus = &bonus
				}
			}
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
		if len(resp.Rows) < maxRows {
			resp.Rows = append(resp.Rows, rw)
		} else {
			resp.ScanCapped = true // a match was dropped because the page is full
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

	// scanBudget bounds the full-walk (kind-only / unfiltered) traversal so a
	// rare-kind query can't scan the whole chain set. With no post-filter
	// (kind=all) the budget is just `max` (every visited tip is a result);
	// a kind filter gets the larger ceiling to give matches a chance to surface.
	// The indexed-scan paths are naturally bounded and don't use a budget.
	scanBudget := maxRows
	if kind != kindAll {
		scanBudget = maxRowsCeiling
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
		scanned := 0
		err1 = rdr.IterateChainedOutputs(func(o ledger.OutputWithChainID) bool {
			scanned++
			process(&o)
			return true
		}, scanBudget)
		// conservative: hitting the budget means the scan was bounded and
		// matching chains beyond it may exist (false even-if exactly budget
		// chains exist is acceptable — better to over-warn than imply complete).
		if scanned >= scanBudget {
			resp.ScanCapped = true
		}
		return err1
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	// sort the returned page by balance desc (first-slice default; richer
	// sort options come later).
	sort.Slice(resp.Rows, func(i, j int) bool {
		return resp.Rows[i].Balance > resp.Rows[j].Balance
	})
	resp.Returned = len(resp.Rows)

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
	EnforceFreezeBounds  bool   `json:"enforce_freeze_bounds"`
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
					EnforceFreezeBounds:  sd.IsFreezeBoundsEnforced(),
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
// delegate lock at index 2, then a mine lock at index 2, else generic.
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
		if _, err := ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib); err == nil {
			rw.Kind = kindSequencer
			rw.Frozen = uint64(o.Output.FrozenCoverage(0))
			si := &sequencerInfo{
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
			LastFrozenEpoch:              dOut.LastFrozenEpoch,
			StatusAtLRB:                  delegationStatusAtLRB(&dOut, lrbSlot),
		}
		return rw
	}

	if lockBytes, err := o.Output.ConstraintAt(ledger.ConstraintIndexLock); err == nil && len(lockBytes) > 0 {
		if ml, err := ledger.MineLockFromBytesWithLib(lockBytes, lib); err == nil {
			// mined is R_init - R off the lock's own counter: A grows with the
			// slot, so transits cannot be multiplied by a single A
			var mined uint64
			if rInit := lib.Constants.MineRemainingInit; rInit > ml.R {
				mined = rInit - ml.R
			}
			rw.Kind = kindMine
			rw.Mine = &mineInfo{
				MinedTransactions: cc.TransitionCounter,
				MinedAmount:       mined,
				Remaining:         ml.R,
			}
			return rw
		}
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
		return fmt.Sprintf("frozen. Safe revocation in %s (slot %d)",
			humanDur(time.Duration(int64(from)-int64(lrbSlot))*slotDur), from)
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
