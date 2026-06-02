package multistate

import (
	"encoding/hex"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
)

type (
	// RootRecordJSONAble mirrors the persistent RootRecord (Root + SequencerID).
	// Aggregates that used to live here moved onto the stem output and are
	// exposed via BranchDataJSONAble instead.
	RootRecordJSONAble struct {
		Root        string `json:"root"`
		SequencerID string `json:"sequencer_id"`
	}

	BranchDataJSONAble struct {
		Root                 RootRecordJSONAble `json:"root"`
		StemOutputIndex      byte               `json:"stem_output_index"`
		SequencerOutputIndex byte               `json:"sequencer_output_index"`
		OnChainAmount        uint64             `json:"on_chain_amount"`
		BranchInflation      uint64             `json:"branch_inflation"`
		// Projected from the stem output (metadata-refactor §5).
		Supply          uint64 `json:"supply"`
		TotalCoverage   uint64 `json:"total_coverage"`
		CoverageDelta   uint64 `json:"coverage_delta"`
		FrozenCoverage  uint64 `json:"frozen_coverage"`
		SlotInflation   uint64 `json:"slot_inflation"`
		NumConfirmedTransactions uint32 `json:"num_confirmed_transactions"`
		NumSeqTransactions uint32 `json:"num_seq_transactions"`
		NumSeq             uint32 `json:"num_seq"`
		BaselineRoot    string `json:"baseline_root"`
	}
)

func (r *RootRecord) JSONAble() *RootRecordJSONAble {
	return &RootRecordJSONAble{
		Root:        r.Root.String(),
		SequencerID: r.SequencerID.StringHex(),
	}
}

func (r *RootRecordJSONAble) Parse() (*RootRecord, error) {
	ret := &RootRecord{}
	var err error
	rootBin, err := hex.DecodeString(r.Root)
	if err != nil {
		return nil, err
	}
	ret.Root, err = common.VectorCommitmentFromBytes(ledger.CommitmentModel, rootBin)
	if err != nil {
		return nil, err
	}
	ret.SequencerID, err = base.ChainIDFromHexString(r.SequencerID)
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// Lines renders the same human-readable summary as BranchData.lines, used by
// proxi commands that only have the JSON DTO (e.g. proxi node lrb).
//
// The healthy-coverage fraction is passed in (numerator/denominator) rather
// than read from the ledger singleton: the wasm-style proxi wallet has no
// singleton, only the ledger constants it fetched over the API. Pass 0/0 to
// omit the healthy line.
func (b *BranchDataJSONAble) Lines(healthyCoverageNumerator, healthyCoverageDenominator uint64, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	var frozenPct float32
	if b.Supply > 0 {
		frozenPct = (float32(b.FrozenCoverage) * 100) / float32(b.Supply)
	}
	ret.Add("sequencer id:    %s", b.Root.SequencerID).
		Add("supply:          %s", util.Th(b.Supply)).
		Add("coverage delta:  %s", util.Th(b.CoverageDelta)).
		Add("total coverage:  %s", util.Th(b.TotalCoverage)).
		Add("frozen coverage: %s (%.2f%s of supply)", util.Th(b.FrozenCoverage), frozenPct, "%")
	if healthyCoverageDenominator > 0 {
		frac := global.Fraction{Numerator: int(healthyCoverageNumerator), Denominator: int(healthyCoverageDenominator)}
		ret.Add("healthy(%s):     %v", frac.String(), global.IsHealthyCoverageDelta(b.CoverageDelta, b.Supply, frac))
	}
	return ret
}

func (b *BranchDataJSONAble) LinesVerbose(healthyCoverageNumerator, healthyCoverageDenominator uint64, prefix ...string) *lines.Lines {
	ret := b.Lines(healthyCoverageNumerator, healthyCoverageDenominator, prefix...)
	ret.Add("root: %s", b.Root.Root).
		Add("slot inflation:   %s", util.Th(b.SlotInflation)).
		Add("num confirmed transactions: %d", b.NumConfirmedTransactions).
		Add("num sequencer transactions: %d", b.NumSeqTransactions).
		Add("num sequencers:   %d", b.NumSeq).
		Add("baseline root:    %s", b.BaselineRoot)
	return ret
}

func (br *BranchData) JSONAble() *BranchDataJSONAble {
	return &BranchDataJSONAble{
		Root:                 *br.RootRecord.JSONAble(),
		StemOutputIndex:      br.Stem.ID.Index(),
		SequencerOutputIndex: br.SequencerOutput.ID.Index(),
		OnChainAmount:        br.SequencerOutput.Output.TokenBalance(),
		BranchInflation:      br.SequencerOutput.Output.Inflation(),
		Supply:               br.Supply,
		TotalCoverage:        br.TotalCoverage,
		CoverageDelta:        br.CoverageDelta,
		FrozenCoverage:       br.FrozenCoverage,
		SlotInflation:        br.SlotInflation,
		NumConfirmedTransactions:      br.NumConfirmedTransactions,
		NumSeqTransactions:   br.NumSeqTransactions,
		NumSeq:               br.NumSeq,
		BaselineRoot:         hex.EncodeToString(br.BaselineRoot),
	}
}
