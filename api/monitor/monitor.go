// Package monitor serves the state-of-the-network page: a high-level overview
// of where the Proxima ledger and network stand — supply and distribution,
// fair-launch mining progress, and decentralization. Aggregate-only; per-chain
// and per-transaction browsing stay in the chain / DAG explorers.
//
// Prototype for spec 0 (claude/monitor.md). Values come in three freshness
// tiers and every one of them is served with its own as-of stamp:
//
//	live       — LRB aggregates, mine chain tip, sequencers: computed per request
//	periodic   — the full-state census: collected by a background goroutine
//	historical — the mine chain back-walk: collected by the same goroutine
//
// The handler never traverses the state: it serializes what the collectors
// have already produced, so a slow or failed collector leaves its section
// stale (and visibly so) instead of stalling the page.
package monitor

import (
	"context"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/bits"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/logo"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

//go:embed monitor.html
var monitorHTML []byte

// monitorPage is the page with the logo substituted in: the bare mark inlined
// in the header, where its `currentColor` strokes take the page's ink color
// (the Proxima star keeps the red it names), and again as a data-URI favicon.
// The mark rather than a lockup: the masthead already spells the name out.
var monitorPage = logo.Page(monitorHTML, logo.MarkOnLight, logo.MarkOnLight)

// Env is what the monitor needs from the node: the LRB state and branch data
// for the live tier, the txstore for the mine chain back-walk, and the node
// context/logger for the background collector.
type Env interface {
	global.Logging
	Ctx() context.Context
	LatestReliableState() (multistate.SugaredStateReader, error)
	GetLatestReliableBranch() *multistate.BranchData
	LatestBranchSlot() uint32
	BranchDataForSlot(slot uint32) []*multistate.BranchData
	TxBytesStore() global.TxBytesStore
	GetConnectivityMatrix() *api.ConnectivityMatrix
	// SubscribeMiningTx delivers every fair-launch mine-chain transit the node
	// accepts, as raw bytes, at arrival time.
	SubscribeMiningTx(fun func(txid base.TransactionID, txBytes []byte) bool)
}

const (
	// censusPeriod is how often the full-state census runs. Deliberately
	// conservative for the prototype: the point of the prototype is to measure
	// what the pass actually costs before choosing a real cadence.
	censusPeriod = 5 * time.Minute
	// mineHistoryDepth is how many mine chain transits are reported, and the
	// sample the mean pace is averaged over.
	mineHistoryDepth = 32
	// mineHistoryMaxSteps bounds the back-walk, which runs past the reported
	// depth to cover the counting window. Each step is one txstore read plus a
	// parse, so this is the cost ceiling of the historical tier.
	mineHistoryMaxSteps = 512
	// mineCountWindow is the span the mined-transaction count covers.
	mineCountWindow = time.Hour
	// topN is how many biggest accounts / sequencers are reported.
	topN = 20
	// activeSequencerSlots is how recently a sequencer must have produced a
	// branch to count as active.
	activeSequencerSlots = 30
	// contestWindowSlots is how far back observed transits stay in the contest
	// window. Wide enough to hold several transits at any plausible pace, short
	// enough that "who is mining right now" means it.
	contestWindowSlots = 120
	// maxObservations caps the contest window. The mine-transit shape is
	// forgeable, so the arrival rate is not bounded by the mining pace: this is
	// what keeps a flood of forgeries from growing the window without limit.
	maxObservations = 512
)

// Monitor holds the asynchronously collected sections. The live section is not
// held here — it is cheap enough to compute per request.
type Monitor struct {
	env     Env
	mutex   sync.RWMutex
	census  *censusSection
	mineHis *mineHistorySection
	// observed is the contest window: mine-chain transits seen on the wire,
	// newest last. Written by the stream subscription, read per request.
	observed []mineObservation
}

// mineObservation is one transit seen on the mining stream. Transits that lost
// the race are here and nowhere else — the mine chain in the state records only
// the winners — which is what makes the number of competing miners knowable.
type mineObservation struct {
	slot        uint32
	miner       string // controller of the minted output, hex
	difficulty  uint64 // B carried by the successor
	predecessor base.OutputID
	verified    bool // proof of work checked against the predecessor
	when        time.Time
}

// Register wires the monitor page and its JSON endpoint, and starts the
// background collector.
func Register(addHandler func(string, func(http.ResponseWriter, *http.Request)), env Env) *Monitor {
	m := &Monitor{env: env}
	addHandler(api.PathMonitor, servePage)
	addHandler(api.PathMonitorData, m.serveData)
	env.SubscribeMiningTx(m.observeMiningTx)
	go m.collectLoop()
	return m
}

func servePage(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(monitorPage)
}

// errNoLRB is returned while the node has no latest reliable branch yet — the
// monitor has nothing to report until it does.
var errNoLRB = errors.New("no LRB available (node still syncing)")

// ---------------------------------------------------------------- JSON shapes

type response struct {
	api.Error
	Live    *liveSection        `json:"live,omitempty"`
	Census  *censusSection      `json:"census,omitempty"`
	MineHis *mineHistorySection `json:"mine_history,omitempty"`
}

// asOf stamps every collected section: the ledger slot it reflects, the wall
// clock when it was produced, and how long producing it took. The page renders
// the age from this rather than assuming any refresh rate.
type asOf struct {
	Slot       uint32 `json:"slot"`
	Unix       int64  `json:"unix"`
	DurationMs int64  `json:"duration_ms"`
}

type liveSection struct {
	AsOf asOf `json:"as_of"`

	// ledger
	LRBID       string `json:"lrbid"`
	LRBDashed   string `json:"lrb_dashed"`
	LRBSlot     uint32 `json:"lrb_slot"`
	CurrentSlot uint32 `json:"current_slot"`
	SlotsBehind uint32 `json:"slots_behind"`

	InitialSupply       uint64 `json:"initial_supply"`
	Supply              uint64 `json:"supply"`
	TotalCoverage       uint64 `json:"total_coverage"`
	CoverageDelta       uint64 `json:"coverage_delta"`
	FrozenCoverage      uint64 `json:"frozen_coverage"`
	SlotInflation       uint64 `json:"slot_inflation"`
	BranchInflationBase uint64 `json:"branch_inflation_base"`

	NumConfirmedTransactions uint32 `json:"num_confirmed_transactions"`
	NumSeqTransactions       uint32 `json:"num_seq_transactions"`
	NumSeq                   uint32 `json:"num_seq"`

	// health: the branch is healthy while coverage delta >= fraction * supply
	HealthyNumerator   uint64 `json:"healthy_numerator"`
	HealthyDenominator uint64 `json:"healthy_denominator"`
	Healthy            bool   `json:"healthy"`
	// HealthyCoverageNeeded is the coverage delta the healthy threshold demands
	// at this supply — the yardstick the decentralization metric is stated
	// against.
	HealthyCoverageNeeded uint64 `json:"healthy_coverage_needed"`

	// AnnualChainInflationCap and AnnualBranchBonusCap are the ceiling on the
	// inflation of the current supply over the year following the current slot:
	// the most chain inflation a fixed amount can earn, plus the largest branch
	// bonus every slot of that year could pay. Mined emission is not inflation
	// of the existing supply and is excluded.
	AnnualChainInflationCap uint64  `json:"annual_chain_inflation_cap"`
	AnnualBranchBonusCap    uint64  `json:"annual_branch_bonus_cap"`
	AnnualInflationCapRate  float64 `json:"annual_inflation_cap_rate"`

	Ledger     ledgerConstants `json:"ledger"`
	FairLaunch fairLaunchLive  `json:"fair_launch"`
	Network    networkLive     `json:"network"`
}

// ledgerConstants identifies the ledger and its clock: what a slot is worth in
// wall-clock time, when slot 0 was, and which library version is in force.
// Fixed for the life of the ledger except the library hash, which changes at an
// upgrade slot.
type ledgerConstants struct {
	GenesisTimeUnix    int64   `json:"genesis_time_unix"`
	TicksPerSlot       uint64  `json:"ticks_per_slot"`
	TickDurationMs     float64 `json:"tick_duration_ms"`
	SlotDurationMs     int64   `json:"slot_duration_ms"`
	TokenName          string  `json:"token_name"`
	MotesPerToken      uint64  `json:"motes_per_token"`
	LibraryHash        string  `json:"library_hash"`
	LibraryUpgradeSlot uint32  `json:"library_upgrade_slot"`
}

// fairLaunchLive is the state of the fair launch: emission read off the single
// mine chain output in the LRB, plus what the mining stream says about the
// contest for the next transit.
type fairLaunchLive struct {
	Present           bool   `json:"present"` // false if the mine chain is absent from this state
	MinedTransactions uint64 `json:"mined_transactions"`
	// MinedAmount is R_init - R, read off the mine lock's own remaining counter.
	// That counter is what the lock enforces, so it is the authoritative record
	// of how much has been emitted — preferred over multiplying the chain's
	// transition counter by A, which only agrees if every transition minted.
	MinedAmount  uint64 `json:"mined_amount"`
	Remaining    uint64 `json:"remaining"`     // R, still mintable
	MintableInit uint64 `json:"mintable_init"` // R_init, the whole mintable budget
	Ceiling      uint64 `json:"ceiling"`       // T = I + R_init
	Difficulty   uint64 `json:"difficulty"`    // B, current
	LastTxSlot   uint32 `json:"last_txslot"`   // slot of the latest transit

	// constants
	// Amount is A at the latest transit's slot. A is not constant: it is flat at
	// AmountBase up to RampStartSlot, then grows by AmountPerSlot each slot.
	Amount          uint64 `json:"amount"`
	AmountBase      uint64 `json:"amount_base"`
	RampStartSlot   uint32 `json:"ramp_start_slot"`
	AmountPerSlot   uint64 `json:"amount_per_slot"`
	MinPace         uint64 `json:"min_pace"`
	TargetPace      uint64 `json:"target_pace"`
	FloorDifficulty uint64 `json:"floor_difficulty"`
	MaxDifficulty   uint64 `json:"max_difficulty"`

	// Contest is what the mining stream says about the race for the next
	// transit. Absent until a transit is observed.
	Contest *mineContest `json:"contest,omitempty"`

	// BootstrapCapital is what the fair launch has to overtake: the genesis
	// supply plus the most chain inflation it could have earned since. Taken as
	// a cap, so its growth per slot is constant — the genesis rate — which is
	// what makes the milestone projections a straight line intersection.
	BootstrapCapital uint64  `json:"bootstrap_capital"`
	BootstrapPerSlot uint64  `json:"bootstrap_per_slot"`
	BootstrapShare   float64 `json:"bootstrap_share"`
	MinedShare       float64 `json:"mined_share"`
}

// mineContest is derived from the mining stream: the transits the node saw
// offered, winners and losers alike. The state holds only winners, so this is
// the only place the number of miners actually competing shows up.
type mineContest struct {
	// CompetingMiners is the number of distinct miners whose transits passed
	// the proof-of-work check within the window.
	CompetingMiners int `json:"competing_miners"`
	// Difficulty is B carried by the most recent verified transit — the live
	// reading, ahead of what the confirmed mine chain shows.
	Difficulty uint64 `json:"difficulty"`
	// Submissions is how many verified transits arrived in the window, and
	// MaxRacingSamePredecessor how many of them raced for the same
	// predecessor: 1 means no contest, higher means real competition.
	Submissions              int `json:"submissions"`
	MaxRacingSamePredecessor int `json:"max_racing_same_predecessor"`
	// Rejected counts arrivals that claimed to be mine transits but failed the
	// check. The shape is forgeable, so this is expected to be non-zero on a
	// public network and is shown rather than hidden.
	Rejected     int   `json:"rejected"`
	WindowSlots  int   `json:"window_slots"`
	LastSeenUnix int64 `json:"last_seen_unix,omitempty"`
}

type networkLive struct {
	NumSequencers       int    `json:"num_sequencers"`
	NumSequencersActive int    `json:"num_sequencers_active"`
	TotalOnSequencers   uint64 `json:"total_on_sequencers"`
	ActiveOnSequencers  uint64 `json:"active_on_sequencers"`
	NumDelegations      int    `json:"num_delegations"`
	DelegatedCapital    uint64 `json:"delegated_capital"`

	// nodes: what this node has evidence of, never a census of what exists
	NumNodes          int   `json:"num_nodes"`
	NumNodesSequencer int   `json:"num_nodes_sequencer"`
	NodesCapturedUnix int64 `json:"nodes_captured_unix"`

	// SequencersToStop is the smallest number of sequencers whose removal (by
	// descending branch coverage delta) drops the remaining coverage delta
	// below the healthy threshold. 0 when the branch is already unhealthy.
	//
	// The subtraction is a proxy, not an identity: competing branches each
	// cover the whole slot, so per-sequencer coverage deltas do not partition
	// the branch's. It ranks sequencers by consensus weight and answers "how
	// few of the heaviest" — which weight to rank by is still open.
	SequencersToStop int     `json:"sequencers_to_stop"`
	TopOneShare      float64 `json:"top_one_share"`
	TopThreeShare    float64 `json:"top_three_share"`

	Sequencers []sequencerRow `json:"sequencers"`
}

type sequencerRow struct {
	ChainID          string `json:"chain_id"`
	Name             string `json:"name"`
	Balance          uint64 `json:"balance"`
	DelegatedCapital uint64 `json:"delegated_capital"`
	NumDelegations   int    `json:"num_delegations"`
	LastActiveSlot   uint32 `json:"last_active_slot"`
	Active           bool   `json:"active"`
	// CoverageDelta is this sequencer's branch coverage delta in the last
	// settled slot; nil when it produced no branch there.
	CoverageDelta *uint64 `json:"coverage_delta,omitempty"`
}

// censusSection is the periodic full-state pass: everything that needs the
// output itself parsed. Accounts are counted as distinct controllers
// (index-values entry 0), never as outputs.
//
// The UTXO set is partitioned three ways and the counts are exhaustive:
// chained, non-chained under a plain signature lock, and non-chained under any
// other (conditional) lock.
//
// Even the plain counts belong here rather than being answered per request: the
// trie keeps no maintained per-node counts, so counting anything at all means
// walking the whole state.
type censusSection struct {
	AsOf asOf `json:"as_of"`

	NumUTXOs       int `json:"num_utxos"`
	NumControllers int `json:"num_controllers"`
	NumChained     int `json:"num_chained"`
	NumSigLock     int `json:"num_siglock"`
	NumConditional int `json:"num_conditional"`

	TotalBalance      uint64 `json:"total_balance"`
	OnChainBalance    uint64 `json:"on_chain_balance"`
	NonChainedBalance uint64 `json:"non_chained_balance"`

	Classes []classRow `json:"classes"`
}

type classRow struct {
	Class         string  `json:"class"`
	NumUTXOs      int     `json:"num_utxos"`
	Balance       uint64  `json:"balance"`
	ShareOfSupply float64 `json:"share_of_supply"`
}

// mineHistorySection is the mine chain back-walk: the observed pace and
// difficulty over the most recent transits, which the state alone cannot show
// (it holds only the tip).
type mineHistorySection struct {
	AsOf asOf `json:"as_of"`

	Transits []mineTransit `json:"transits"` // newest first, capped at mineHistoryDepth
	// Depth is how many transits are reported, MeanPace their mean slot gap and
	// PaceWindow how many gaps that mean is taken over.
	Depth      int     `json:"depth"`
	MeanPace   float64 `json:"mean_pace"`
	PaceWindow int     `json:"pace_window"`
	NumMiners  int     `json:"num_miners"`
	// MinedLastHour counts the transits settled within WindowSlots of the slot
	// the walk started from. The walk continues past the reported depth to
	// cover the window, so this is not limited by Depth.
	MinedLastHour int `json:"mined_last_hour"`
	WindowSlots   int `json:"window_slots"`
	// TruncatedBy is set when the walk stopped early, naming the reason.
	TruncatedBy string `json:"truncated_by,omitempty"`
}

type mineTransit struct {
	Slot       uint32 `json:"slot"`
	Pace       int    `json:"pace"` // slots since the predecessor transit; 0 for the oldest walked
	Difficulty uint64 `json:"difficulty"`
	Miner      string `json:"miner"` // controller of the minted output, hex
}

// ---------------------------------------------------------------- handler

func (m *Monitor) serveData(w http.ResponseWriter, _ *http.Request) {
	api.SetHeader(w)

	var resp response
	err := util.CatchPanicOrError(func() error {
		live, err := m.collectLive()
		if err != nil {
			return err
		}
		resp.Live = live
		return nil
	})
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}

	m.mutex.RLock()
	resp.Census, resp.MineHis = m.census, m.mineHis
	m.mutex.RUnlock()

	respBin, err := json.MarshalIndent(&resp, "", "  ")
	if err != nil {
		api.WriteErr(w, err.Error())
		return
	}
	_, _ = w.Write(respBin)
}

// ---------------------------------------------------------------- live tier

func (m *Monitor) collectLive() (*liveSection, error) {
	start := time.Now()
	br := m.env.GetLatestReliableBranch()
	if br == nil {
		return nil, errNoLRB
	}
	rdr, err := m.env.LatestReliableState()
	if err != nil {
		return nil, err
	}

	lrbTxid := br.TxID()
	lrbSlot := br.Slot()
	lib := ledger.L(base.MaxSlot)
	frac := global.FractionHealthyBranchAt(lrbSlot)

	ret := &liveSection{
		LRBID:                    lrbTxid.StringHex(),
		LRBDashed:                lrbTxid.String(),
		LRBSlot:                  lrbSlot,
		CurrentSlot:              ledger.SlotNow(),
		InitialSupply:            lib.Constants.InitialSupply,
		Supply:                   br.Supply,
		TotalCoverage:            br.TotalCoverage,
		CoverageDelta:            br.CoverageDelta,
		FrozenCoverage:           br.FrozenCoverage,
		SlotInflation:            br.SlotInflation,
		BranchInflationBase:      lib.BranchInflationBonusBase(lrbSlot),
		NumConfirmedTransactions: br.NumConfirmedTransactions,
		NumSeqTransactions:       br.NumSeqTransactions,
		NumSeq:                   br.NumSeq,
		HealthyNumerator:         uint64(frac.Numerator),
		HealthyDenominator:       uint64(frac.Denominator),
		Healthy:                  br.IsHealthy(),
		HealthyCoverageNeeded:    br.Supply / uint64(frac.Denominator) * uint64(frac.Numerator),
		Ledger:                   ledgerHeader(lib),
	}
	if ret.CurrentSlot > lrbSlot {
		ret.SlotsBehind = ret.CurrentSlot - lrbSlot
	}
	fillInflationCap(ret, lib)

	// One chain walk feeds both the mining tip and the network section: the
	// mine chain, the sequencers, and the delegations pointing at them.
	if err = m.walkChains(rdr, lib, br, ret); err != nil {
		return nil, err
	}
	ret.FairLaunch.Contest = m.contest(lrbSlot)
	m.fillNetworkAggregates(ret, lrbSlot)

	ret.AsOf = asOf{Slot: lrbSlot, Unix: time.Now().Unix(), DurationMs: time.Since(start).Milliseconds()}
	return ret, nil
}

// fillInflationCap projects the inflation ceiling for the year ahead. Both
// components are upper bounds nothing can exceed: chain inflation is what the
// whole supply would earn if every token sat on a chain inflated at every slot,
// and the branch bonus is the base — the maximum the VRF draw can reach — taken
// once per slot. The bonus base is read at the starting slot, which is exact
// while it stays flat over the year.
func fillInflationCap(ret *liveSection, lib *ledger.Library) {
	slotsPerYear := uint32(lib.SlotsPerYear())
	ret.AnnualChainInflationCap = lib.ChainInflationMultiStep(ret.Supply, ret.CurrentSlot, slotsPerYear)
	ret.AnnualBranchBonusCap = lib.BranchInflationBonusBase(ret.CurrentSlot) * uint64(slotsPerYear)
	if ret.Supply > 0 {
		ret.AnnualInflationCapRate = float64(ret.AnnualChainInflationCap+ret.AnnualBranchBonusCap) / float64(ret.Supply)
	}
}

// ledgerHeader reports the ledger clock and the library in force. Clock values
// come from the genesis library: they are immutable, and all ledger-time
// conversion is defined against them.
func ledgerHeader(lib *ledger.Library) ledgerConstants {
	gen := ledger.L(0)
	ret := ledgerConstants{
		GenesisTimeUnix: gen.GenesisTime().Unix(),
		TicksPerSlot:    gen.TicksPerSlot,
		TickDurationMs:  float64(gen.TickDuration) / float64(time.Millisecond),
		SlotDurationMs:  ledger.SlotDuration().Milliseconds(),
		TokenName:       base.BaseTokenName,
		MotesPerToken:   base.PROX,
		LibraryHash:     hex.EncodeToString(lib.Constants.Hash[:]),
	}
	if ucd := lib.UpgradeChainData(); ucd != nil {
		ret.LibraryUpgradeSlot = ucd.UpgradeSlot
	}
	return ret
}

// walkChains collects the mine chain tip, every sequencer and every delegation
// in one pass over the chain tips.
func (m *Monitor) walkChains(rdr multistate.SugaredStateReader, lib *ledger.Library, br *multistate.BranchData, ret *liveSection) error {
	// delegated capital per target sequencer, resolved after the walk
	delegatedTo := make(map[base.ChainID]uint64)
	delegationsTo := make(map[base.ChainID]int)
	seqRows := make(map[base.ChainID]*sequencerRow)

	err := rdr.IterateChainedOutputs(func(o ledger.OutputWithChainID) bool {
		if o.ChainID == base.MineChainID {
			fillMining(&ret.FairLaunch, &o, lib)
			return true
		}
		if seqBytes, err := o.Output.ConstraintAt(ledger.SequencerConstraintFixedIndex); err == nil && len(seqBytes) > 0 {
			if _, err = ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib); err == nil {
				row := &sequencerRow{
					ChainID:        o.ChainID.StringHex(),
					Balance:        o.Output.TokenBalance(),
					LastActiveSlot: o.ID.Slot(),
				}
				if sd, err := ledger.ParseSequencerData(o.Output); err == nil {
					row.Name = sd.Name()
				}
				seqRows[o.ChainID] = row
				return true
			}
		}
		if dOut, ok := ledger.DelegationOutputFromOutputWithChainIDWithLib(&o, lib); ok {
			delegatedTo[dOut.Target] += o.Output.TokenBalance()
			delegationsTo[dOut.Target]++
			ret.Network.NumDelegations++
			ret.Network.DelegatedCapital += o.Output.TokenBalance()
		}
		return true
	})
	if err != nil {
		return err
	}

	// per-sequencer branch coverage delta from the last settled slot (the slot
	// before the latest one carrying any branch, so competing branches are all
	// present) — the same source the chain explorer uses.
	if latest := m.env.LatestBranchSlot(); latest > 1 {
		for _, bd := range m.env.BranchDataForSlot(latest - 1) {
			row, ok := seqRows[bd.SequencerID]
			if !ok {
				continue
			}
			if row.CoverageDelta == nil || *row.CoverageDelta < bd.CoverageDelta {
				cd := bd.CoverageDelta
				row.CoverageDelta = &cd
			}
		}
	}

	lrbSlot := br.Slot()
	for chainID, row := range seqRows {
		row.DelegatedCapital = delegatedTo[chainID]
		row.NumDelegations = delegationsTo[chainID]
		row.Active = lrbSlot < row.LastActiveSlot+activeSequencerSlots
		ret.Network.Sequencers = append(ret.Network.Sequencers, *row)
	}
	if ret.FairLaunch.Present {
		fl := &ret.FairLaunch
		// bootstrap capital: genesis plus the ceiling on the chain inflation it
		// could have earned from slot 0 to here
		fl.BootstrapCapital = ret.InitialSupply + lib.ChainInflationMultiStep(ret.InitialSupply, 0, lrbSlot)
		fl.BootstrapPerSlot = lib.ChainInflationOneSlot(ret.InitialSupply, 0)
		if ret.Supply > 0 {
			fl.MinedShare = float64(fl.MinedAmount) / float64(ret.Supply)
			fl.BootstrapShare = float64(fl.BootstrapCapital) / float64(ret.Supply)
		}
	}
	return nil
}

func fillMining(mn *fairLaunchLive, o *ledger.OutputWithChainID, lib *ledger.Library) {
	lockBytes, err := o.Output.ConstraintAt(ledger.ConstraintIndexLock)
	if err != nil {
		return
	}
	ml, err := ledger.MineLockFromBytesWithLib(lockBytes, lib)
	if err != nil {
		return
	}
	c := lib.Constants
	rInit := c.MineRemainingInit
	var mined uint64
	if rInit > ml.R {
		mined = rInit - ml.R
	}
	*mn = fairLaunchLive{
		Present:           true,
		MinedTransactions: o.ChainConstraint.TransitionCounter,
		MinedAmount:       mined,
		Remaining:         ml.R,
		MintableInit:      rInit,
		Ceiling:           c.InitialSupply + rInit,
		Difficulty:        ml.B,
		LastTxSlot:        o.ID.Slot(),
		Amount:            c.MineAmountAtSlot(o.ID.Slot()),
		AmountBase:        c.MineAmountBase,
		RampStartSlot:     c.MineRampStartSlot,
		AmountPerSlot:     c.MineAmountPerSlot,
		MinPace:           c.MineMinPace,
		TargetPace:        c.MineTargetPace,
		FloorDifficulty:   c.MineFloorDifficulty,
		MaxDifficulty:     c.MineMaxDifficulty,
	}
}

// fillNetworkAggregates derives the totals and the decentralization figures
// from the collected sequencer rows.
func (m *Monitor) fillNetworkAggregates(ret *liveSection, lrbSlot uint32) {
	nw := &ret.Network
	sort.Slice(nw.Sequencers, func(i, j int) bool {
		return nw.Sequencers[i].Balance+nw.Sequencers[i].DelegatedCapital >
			nw.Sequencers[j].Balance+nw.Sequencers[j].DelegatedCapital
	})
	nw.NumSequencers = len(nw.Sequencers)
	for i := range nw.Sequencers {
		nw.TotalOnSequencers += nw.Sequencers[i].Balance
		if nw.Sequencers[i].Active {
			nw.NumSequencersActive++
			nw.ActiveOnSequencers += nw.Sequencers[i].Balance
		}
	}
	if len(nw.Sequencers) > topN {
		nw.Sequencers = nw.Sequencers[:topN]
	}

	// Consensus weight = branch coverage delta share in the last settled slot;
	// remove sequencers heaviest-first until what remains can no longer meet
	// the healthy threshold. See the field comment on the proxy this makes.
	weights := make([]uint64, 0, len(nw.Sequencers))
	var totalWeight uint64
	for i := range nw.Sequencers {
		if cd := nw.Sequencers[i].CoverageDelta; cd != nil {
			weights = append(weights, *cd)
			totalWeight += *cd
		}
	}
	sort.Slice(weights, func(i, j int) bool { return weights[i] > weights[j] })
	if totalWeight > 0 {
		nw.TopOneShare = float64(weights[0]) / float64(totalWeight)
		var top3 uint64
		for i := 0; i < len(weights) && i < 3; i++ {
			top3 += weights[i]
		}
		nw.TopThreeShare = float64(top3) / float64(totalWeight)

		remaining := ret.CoverageDelta
		for _, w := range weights {
			if !global.IsHealthyBranchAt(lrbSlot, remaining, ret.Supply) {
				break
			}
			if w > remaining {
				w = remaining
			}
			remaining -= w
			nw.SequencersToStop++
		}
	}

	if cm := m.env.GetConnectivityMatrix(); cm != nil {
		nw.NumNodes = len(cm.Nodes)
		nw.NodesCapturedUnix = cm.CapturedAt / int64(time.Second)
		for _, contribution := range cm.Contribution {
			if contribution > 0 {
				nw.NumNodesSequencer++
			}
		}
	}
}

// ------------------------------------------------------------- mining stream

// observeMiningTx folds one streamed transit into the contest window. It runs
// on the node's event dispatch, so it stays cheap: a parse, one predecessor
// lookup and a hash.
//
// The node relays transits without constraint-validating them, so the proof of
// work is unchecked and the mine-transit shape is forgeable. Counting miners
// off unverified arrivals would let anyone inflate the figure, so the work is
// checked here against the predecessor the transit spends; only transits that
// pass are counted as competitors.
func (m *Monitor) observeMiningTx(txid base.TransactionID, txBytes []byte) bool {
	obs := mineObservation{slot: txid.Slot(), when: time.Now()}

	err := util.CatchPanicOrError(func() error {
		tx, err := transaction.Parse(txBytes)
		if err != nil {
			return err
		}
		succ, err := tx.ProducedOutputAt(0)
		if err != nil {
			return err
		}
		cc := succ.ChainConstraint()
		if cc == nil || cc.ChainID != base.MineChainID {
			return errNotMineTransit
		}
		lib := ledger.L(base.MaxSlot)
		ml, err := ledger.MineLockFromBytesWithLib(succ.MustAt(int(ledger.ConstraintIndexLock)), lib)
		if err != nil {
			return err
		}
		obs.difficulty = ml.B
		if obs.predecessor, err = tx.InputAt(cc.PredecessorInputIndex); err != nil {
			return err
		}
		obs.miner = minerOf(tx, 0)
		obs.verified = m.checkMineWork(txBytes, obs.predecessor, txid.Slot(), lib)
		return nil
	})
	if err != nil {
		obs.verified = false
	}

	m.mutex.Lock()
	m.observed = append(m.observed, obs)
	if len(m.observed) > maxObservations {
		m.observed = m.observed[len(m.observed)-maxObservations:]
	}
	m.mutex.Unlock()
	return true
}

// errNotMineTransit marks an arrival that claims the mine-transit shape but
// does not build on the mine chain.
var errNotMineTransit = errors.New("not a mine chain transit")

// checkMineWork verifies the transit's proof of work against the difficulty its
// predecessor demands at this pace. The predecessor is resolved from the
// txstore (streamed transits are persisted before the event) or, for the
// confirmed tip, from the LRB state.
func (m *Monitor) checkMineWork(txBytes []byte, predOID base.OutputID, succSlot uint32, lib *ledger.Library) bool {
	predData := m.mineOutputData(predOID)
	if predData == nil {
		return false
	}
	predOut, err := ledger.OutputFromBytes(predData)
	if err != nil {
		return false
	}
	predLock, err := ledger.MineLockFromBytesWithLib(predOut.MustAt(int(ledger.ConstraintIndexLock)), lib)
	if err != nil {
		return false
	}
	predSlot := predOID.Slot()
	if succSlot < predSlot {
		return false
	}
	needK := lib.Constants.MineRequiredK(predLock.B, uint64(succSlot-predSlot))
	return uint64(trailingZeroBits(blake2b.Sum256(txBytes))) >= needK
}

// mineOutputData returns the raw bytes of a mine chain output, from the LRB
// state if it is the confirmed tip, otherwise from its transaction in the
// txstore.
func (m *Monitor) mineOutputData(oid base.OutputID) []byte {
	if rdr, err := m.env.LatestReliableState(); err == nil {
		if data, ok := rdr.GetUTXO(oid); ok {
			return data
		}
	}
	txid := oid.TransactionID()
	txBytes := m.env.TxBytesStore().GetTxBytes(&txid)
	if len(txBytes) == 0 {
		return nil
	}
	tx, err := transaction.Parse(txBytes)
	if err != nil {
		return nil
	}
	o, err := tx.ProducedOutputAt(oid.Index())
	if err != nil {
		return nil
	}
	return o.Bytes()
}

// minerOf returns the controller of the minted output: the one produced output
// that is not the mine chain successor itself.
func minerOf(tx *transaction.Transaction, chainOutputIndex byte) string {
	for i := 0; i < tx.NumProducedOutputs(); i++ {
		if byte(i) == chainOutputIndex {
			continue
		}
		o, err := tx.ProducedOutputAt(byte(i))
		if err != nil {
			continue
		}
		if iv := o.IndexValues(); len(iv) > 0 && len(iv[0]) > 0 {
			return hex.EncodeToString(iv[0])
		}
	}
	return ""
}

// trailingZeroBits counts trailing zero bits of the hash, which is the
// proof-of-work measure the mine constraint requires.
func trailingZeroBits(h [32]byte) int {
	n := 0
	for i := len(h) - 1; i >= 0; i-- {
		if h[i] == 0 {
			n += 8
			continue
		}
		return n + bits.TrailingZeros8(h[i])
	}
	return n
}

// contest summarizes the window, dropping observations older than it.
func (m *Monitor) contest(lrbSlot uint32) *mineContest {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	keep := m.observed[:0]
	for _, o := range m.observed {
		if o.slot+contestWindowSlots >= lrbSlot {
			keep = append(keep, o)
		}
	}
	m.observed = keep
	if len(m.observed) == 0 {
		return nil
	}

	ret := &mineContest{WindowSlots: contestWindowSlots}
	miners := make(map[string]struct{})
	racing := make(map[base.OutputID]int)
	for _, o := range m.observed {
		if !o.verified {
			ret.Rejected++
			continue
		}
		ret.Submissions++
		if o.miner != "" {
			miners[o.miner] = struct{}{}
		}
		racing[o.predecessor]++
		if o.when.Unix() > ret.LastSeenUnix {
			ret.LastSeenUnix = o.when.Unix()
			ret.Difficulty = o.difficulty
		}
	}
	ret.CompetingMiners = len(miners)
	for _, n := range racing {
		if n > ret.MaxRacingSamePredecessor {
			ret.MaxRacingSamePredecessor = n
		}
	}
	return ret
}

// ------------------------------------------------- periodic + historical tiers

func (m *Monitor) collectLoop() {
	// first pass right away so the page has something beyond the live tier
	m.collectOnce()
	t := time.NewTicker(censusPeriod)
	defer t.Stop()
	for {
		select {
		case <-m.env.Ctx().Done():
			return
		case <-t.C:
			m.collectOnce()
		}
	}
}

func (m *Monitor) collectOnce() {
	err := util.CatchPanicOrError(func() error {
		census, err := m.collectCensus()
		if err != nil {
			return err
		}
		hist := m.collectMineHistory()

		m.mutex.Lock()
		m.census = census
		if hist != nil {
			m.mineHis = hist
		}
		m.mutex.Unlock()
		return nil
	})
	if err != nil {
		m.env.Log().Warnf("[monitor] collector: %v", err)
	}
}

// Account class names, keyed off the lock kind plus the chain constraint.
// Foundries and plain chains share classOtherChain; the mine chain's open
// mineLock is not a holder lock and lands in classOther with the stem.
const (
	classSequencer  = "sequencers"
	classDelegation = "delegations"
	classOtherChain = "other chains"
	classSigLock    = "siglocks"
	classOther      = "other locks"
)

// collectCensus walks the whole LRB state once. Everything it needs — amount,
// lock kind, index values, chain constraint — is carried by the output itself,
// so the controllers partition is never touched.
func (m *Monitor) collectCensus() (*censusSection, error) {
	start := time.Now()
	br := m.env.GetLatestReliableBranch()
	if br == nil {
		return nil, errNoLRB
	}
	rdr, err := m.env.LatestReliableState()
	if err != nil {
		return nil, err
	}
	lib := ledger.L(base.MaxSlot)

	ret := &censusSection{}
	classes := make(map[string]*classRow)
	// distinct controllers seen, for the address count. A set rather than an
	// aggregate: memory is bounded by the number of controllers, not by UTXOs.
	byController := make(map[string]struct{})

	classOf := func(o *ledger.Output, chained bool) string {
		switch o.Lock().Name() {
		case ledger.DelegateLockName:
			return classDelegation
		case ledger.SigLockName:
			if !chained {
				return classSigLock
			}
			// chained sigLock output: a sequencer, or any other chain
			if seqBytes, err := o.ConstraintAt(ledger.SequencerConstraintFixedIndex); err == nil && len(seqBytes) > 0 {
				if _, err := ledger.SequencerConstraintFromBytesWithLib(seqBytes, lib); err == nil {
					return classSequencer
				}
			}
			return classOtherChain
		default:
			return classOther
		}
	}

	// The pass walks the whole state, so it must not outlive a shutdown: the
	// context is polled per output, the same way snapshot writing does it.
	ctx := m.env.Ctx()
	interrupted := false

	err = rdr.IterateUTXOs(func(o ledger.OutputWithID) bool {
		select {
		case <-ctx.Done():
			interrupted = true
			return false
		default:
		}

		amount := o.Output.TokenBalance()
		ret.NumUTXOs++
		ret.TotalBalance += amount

		_, chained := o.ExtractChainID()
		cl := classOf(o.Output, chained)
		switch {
		case chained:
			ret.NumChained++
			ret.OnChainBalance += amount
		default:
			ret.NonChainedBalance += amount
			if cl == classSigLock {
				ret.NumSigLock++
			} else {
				ret.NumConditional++
			}
		}

		row := classes[cl]
		if row == nil {
			row = &classRow{Class: cl}
			classes[cl] = row
		}
		row.NumUTXOs++
		row.Balance += amount

		// Only classes that represent a holder count as an address: the stem, the
		// mine chain and other framework locks carry index values that are not
		// accounts.
		if cl == classOther {
			return true
		}
		// The controller is index-values entry 0. A delegation is indexed under
		// both master and target, so taking it by position rather than by every
		// index value is what keeps one holder from being counted twice.
		iv := o.Output.IndexValues()
		if len(iv) == 0 || len(iv[0]) == 0 {
			return true
		}
		byController[string(iv[0])] = struct{}{}
		return true
	})
	if err != nil {
		return nil, err
	}
	if interrupted {
		return nil, errInterrupted
	}

	ret.NumControllers = len(byController)
	for _, row := range classes {
		if br.Supply > 0 {
			row.ShareOfSupply = float64(row.Balance) / float64(br.Supply)
		}
		ret.Classes = append(ret.Classes, *row)
	}
	sort.Slice(ret.Classes, func(i, j int) bool { return ret.Classes[i].Balance > ret.Classes[j].Balance })

	ret.AsOf = asOf{Slot: br.Slot(), Unix: time.Now().Unix(), DurationMs: time.Since(start).Milliseconds()}
	return ret, nil
}

// errInterrupted marks a census pass abandoned because the node is shutting
// down. The previous pass stays on display rather than being replaced by a
// partial one.
var errInterrupted = errors.New("census pass interrupted by shutdown")

// collectMineHistory walks the mine chain backwards through the txstore. Only
// the tip lives in the state, so pace and difficulty over time can be had no
// other way (short of streaming transits as they happen).
func (m *Monitor) collectMineHistory() *mineHistorySection {
	start := time.Now()
	br := m.env.GetLatestReliableBranch()
	if br == nil {
		return nil
	}
	rdr, err := m.env.LatestReliableState()
	if err != nil {
		return nil
	}
	tip, err := rdr.GetChainOutputWithChainID(base.MineChainID)
	if err != nil {
		return nil
	}
	lib := ledger.L(base.MaxSlot)
	store := m.env.TxBytesStore()

	// the count window is anchored on the LRB slot, not on the tip: if mining
	// has stalled, the last hour genuinely holds fewer transits
	windowSlots := uint32(mineCountWindow / ledger.SlotDuration())
	var cutoff uint32
	if br.Slot() > windowSlots {
		cutoff = br.Slot() - windowSlots
	}
	ret := &mineHistorySection{
		Transits:    make([]mineTransit, 0, mineHistoryDepth),
		WindowSlots: int(windowSlots),
	}
	miners := make(map[string]struct{})

	oid := tip.ID
	for steps := 0; ; steps++ {
		if steps == mineHistoryMaxSteps {
			ret.TruncatedBy = "step cap reached; the count is a lower bound"
			break
		}
		txid := oid.TransactionID()
		txBytes := store.GetTxBytes(&txid)
		if len(txBytes) == 0 {
			ret.TruncatedBy = "txstore does not reach further back"
			break
		}
		tx, err := transaction.Parse(txBytes)
		if err != nil {
			ret.TruncatedBy = "unparseable transaction in the txstore"
			break
		}
		o, err := tx.ProducedOutputAt(oid.Index())
		if err != nil {
			ret.TruncatedBy = "mine output missing from its transaction"
			break
		}
		// the genesis mine output is the chain origin, not a mined transit: it
		// belongs neither in the list nor in the count
		cc := o.ChainConstraint()
		if cc == nil || cc.IsOrigin() {
			ret.TruncatedBy = "reached the genesis mine output"
			break
		}
		if oid.Slot() >= cutoff {
			ret.MinedLastHour++
		} else if len(ret.Transits) >= mineHistoryDepth {
			// past the window and the reported list is full: nothing left to learn
			break
		}
		tr := mineTransit{Slot: oid.Slot()}
		if lockBytes, err := o.ConstraintAt(ledger.ConstraintIndexLock); err == nil {
			if ml, err := ledger.MineLockFromBytesWithLib(lockBytes, lib); err == nil {
				tr.Difficulty = ml.B
			}
		}
		// the minted amount goes to the miner: the one produced output of this
		// transaction that is not the mine chain itself
		for i := 0; i < tx.NumProducedOutputs(); i++ {
			if byte(i) == oid.Index() {
				continue
			}
			po, err := tx.ProducedOutputAt(byte(i))
			if err != nil {
				continue
			}
			iv := po.IndexValues()
			if len(iv) > 0 && len(iv[0]) > 0 {
				tr.Miner = hex.EncodeToString(iv[0])
				miners[tr.Miner] = struct{}{}
				break
			}
		}
		if len(ret.Transits) < mineHistoryDepth {
			ret.Transits = append(ret.Transits, tr)
		}

		// step back to the predecessor mine output
		prev, err := tx.InputAt(cc.PredecessorInputIndex)
		if err != nil {
			ret.TruncatedBy = "predecessor input missing"
			break
		}
		oid = prev
	}

	ret.Depth = len(ret.Transits)
	ret.NumMiners = len(miners)
	// pace: slot gaps between consecutive transits (the list is newest first)
	var sum, n int
	for i := 0; i+1 < len(ret.Transits); i++ {
		gap := int(ret.Transits[i].Slot) - int(ret.Transits[i+1].Slot)
		ret.Transits[i].Pace = gap
		sum += gap
		n++
	}
	ret.PaceWindow = n
	if n > 0 {
		ret.MeanPace = float64(sum) / float64(n)
	}
	ret.AsOf = asOf{Slot: br.Slot(), Unix: time.Now().Unix(), DurationMs: time.Since(start).Milliseconds()}
	return ret
}
