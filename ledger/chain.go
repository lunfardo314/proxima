package ledger

import (
	"bytes"
	_ "embed"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

//go:embed def/chain.easyfl
var chainConstraintSource string

// ChainConstraint is a chain constraint. Always at index 2 in the output tuple.
type ChainConstraint struct {
	// ChainID all-0 for origin
	ChainID base.ChainID
	// Predecessor input index. 0xFF means origin (no predecessor). Serialized as 0x (empty) for origin.
	PredecessorInputIndex byte
	// slot of the origin chain output
	OriginSlot uint32
	// cumulative chain inflation (z64). 0x at origin.
	CumulativeChainInflation uint64
	// cumulative branch inflation bonus (z64). 0x at origin. Non-zero only on sequencer chains.
	CumulativeBranchBonus uint64
	// incremental transition counter (z64). 0x at origin.
	TransitionCounter uint64
	// incremental branch counter (z32). 0x at origin. Increments only on the sequencer output of branch transactions.
	BranchCounter uint32
}

const (
	ChainConstraintName               = "chain"
	chainConstraintTemplateOrigin     = ChainConstraintName + "(0x%s, 0x%s, z32/%d, 0x, 0x, 0x, 0x)"
	chainConstraintTemplateTransition = ChainConstraintName + "(0x%s, 0x%s, z32/%d, z64/%d, z64/%d, z64/%d, z32/%d)"
)

func NewChainConstraint(id base.ChainID, predInputIndex byte, originSlot uint32, cumulativeChainInflation uint64, cumulativeBranchBonus uint64, transitionCounter uint64, branchCounter uint32) *ChainConstraint {
	util.Assertf(uint64(branchCounter) <= transitionCounter, "branchCounter (%d) cannot exceed transitionCounter (%d)", branchCounter, transitionCounter)
	return &ChainConstraint{
		ChainID:                 id,
		PredecessorInputIndex:   predInputIndex,
		OriginSlot:              originSlot,
		CumulativeChainInflation: cumulativeChainInflation,
		CumulativeBranchBonus:   cumulativeBranchBonus,
		TransitionCounter:       transitionCounter,
		BranchCounter:           branchCounter,
	}
}

func NewChainOrigin(startSlot uint32) *ChainConstraint {
	return NewChainConstraint(base.NilChainID, 0xff, startSlot, 0, 0, 0, 0)
}

func (cc *ChainConstraint) IsOrigin() bool {
	return cc.ChainID == base.NilChainID && cc.PredecessorInputIndex == 0xff
}

func (cc *ChainConstraint) Name() string {
	return ChainConstraintName
}

func (cc *ChainConstraint) Bytes() []byte {
	return mustBinFromSource(cc.Source())
}

func (cc *ChainConstraint) String() string {
	chID := "ORIGIN"
	predRefStr := "empty"
	if !cc.IsOrigin() {
		chID = cc.ChainID.String()
		predRefStr = hex.EncodeToString([]byte{cc.PredecessorInputIndex})
	}
	return fmt.Sprintf("%s(%s, predInputIdx=%s, originSlot=%d, cumInflation=%s, cumBranchBonus=%s, txCounter=%d, branchCounter=%d)",
		ChainConstraintName, chID, predRefStr, cc.OriginSlot,
		util.Th(cc.CumulativeChainInflation), util.Th(cc.CumulativeBranchBonus), cc.TransitionCounter, cc.BranchCounter)
}

func (cc *ChainConstraint) Source() string {
	var predRefHex string
	if !cc.IsOrigin() {
		predRefHex = hex.EncodeToString([]byte{cc.PredecessorInputIndex})
	}
	chainIDHex := hex.EncodeToString(cc.ChainID[:])
	// At origin, $3/$4/$5 are 0x (empty bytes). At transitions, use z64/z32 encoding.
	if cc.IsOrigin() {
		return fmt.Sprintf(chainConstraintTemplateOrigin, chainIDHex, predRefHex, cc.OriginSlot)
	}
	return fmt.Sprintf(chainConstraintTemplateTransition,
		chainIDHex, predRefHex, cc.OriginSlot,
		cc.CumulativeChainInflation, cc.CumulativeBranchBonus, cc.TransitionCounter, cc.BranchCounter)
}

func ChainConstraintFromBytes(data []byte) (*ChainConstraint, error) {
	return ChainConstraintFromBytesWithLib(data, L(base.MaxSlot))
}

func ChainConstraintFromBytesWithLib(data []byte, lib *Library) (*ChainConstraint, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 7)
	if err != nil {
		return nil, err
	}
	if sym != ChainConstraintName {
		return nil, fmt.Errorf("ChainConstraintFromBytes: not a chain constraint")
	}

	ret := &ChainConstraint{}
	if ret.ChainID, err = base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, err
	}
	args1 := easyfl.StripDataPrefix(args[1])
	switch len(args1) {
	case 0:
		// origin: empty predecessor reference
		ret.PredecessorInputIndex = 0xff
	case 1:
		ret.PredecessorInputIndex = args1[0]
	default:
		return nil, fmt.Errorf("ChainConstraintFromBytes: wrong predecessor reference length %d", len(args1))
	}
	sl, err := easyfl_util.Uint32FromBytes(easyfl.StripDataPrefix(args[2]))
	if err != nil {
		return nil, err
	}
	ret.OriginSlot = sl

	// $3: cumulative chain inflation (z64)
	args3 := easyfl.StripDataPrefix(args[3])
	if len(args3) > 0 {
		if ret.CumulativeChainInflation, err = easyfl_util.Uint64FromBytes(args3); err != nil {
			return nil, err
		}
	}
	// $4: cumulative branch inflation bonus (z64)
	args4 := easyfl.StripDataPrefix(args[4])
	if len(args4) > 0 {
		if ret.CumulativeBranchBonus, err = easyfl_util.Uint64FromBytes(args4); err != nil {
			return nil, err
		}
	}
	// $5: transition counter (z64)
	args5 := easyfl.StripDataPrefix(args[5])
	if len(args5) > 0 {
		if ret.TransitionCounter, err = easyfl_util.Uint64FromBytes(args5); err != nil {
			return nil, err
		}
	}
	// $6: branch counter (z32)
	args6 := easyfl.StripDataPrefix(args[6])
	if len(args6) > 0 {
		if ret.BranchCounter, err = easyfl_util.Uint32FromBytes(args6); err != nil {
			return nil, err
		}
	}
	if uint64(ret.BranchCounter) > ret.TransitionCounter {
		return nil, fmt.Errorf("ChainConstraintFromBytes: branchCounter (%d) cannot exceed transitionCounter (%d)", ret.BranchCounter, ret.TransitionCounter)
	}
	return ret, nil
}

// NewChainUnlockParams unlock parameters for the chain constraint. 1 byte:
// 0 - successor output index
func NewChainUnlockParams(successorOutputIdx byte) []byte {
	return []byte{successorOutputIdx}
}

// FinishChainUnlockParams discontinues the chain. Empty unlock data.
var FinishChainUnlockParams = []byte{}

func registerChainConstraint(lib *Library) {
	lib.mustRegisterConstraint(ChainConstraintName, 7, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return ChainConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// test origin serialization round-trip
		example := NewChainOrigin(1000)
		back, err := ChainConstraintFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(back.Bytes(), example.Bytes()), "inconsistency in "+ChainConstraintName)
		util.Assertf(back.OriginSlot == 1000, "back.OriginSlot == 1000")
		util.Assertf(back.CumulativeChainInflation == 0, "origin CumulativeChainInflation == 0")
		util.Assertf(back.CumulativeBranchBonus == 0, "origin CumulativeBranchBonus == 0")
		util.Assertf(back.TransitionCounter == 0, "origin TransitionCounter == 0")
		util.Assertf(back.BranchCounter == 0, "origin BranchCounter == 0")

		var chainID base.ChainID
		dummyHash := blake2b.Sum256([]byte("dummy"))
		copy(chainID[:], dummyHash[:])
		{
			chainIDBack, err := base.ChainIDFromBytes(chainID.Bytes())
			util.AssertNoError(err)
			util.Assertf(chainIDBack == chainID, "chainIDBack == chainID")
		}
		{
			// test transition serialization round-trip
			chainConstr := NewChainConstraint(chainID, 0, 1000, 500_000, 100_000, 42, 7)
			chainConstrBack, err := ChainConstraintFromBytesWithLib(chainConstr.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(*chainConstrBack == *chainConstr, "*chainConstrBack == *chainConstr")
		}
	})
}

// evalEnforceFrozenCoverageOnNonDelegationChain runs on every produced
// chain output that is not a delegation. The frozen-coverage vector
// size is sourced from THIS chain's sequencer constraint at slot 4
// (if attached). Regular chains carry no sequencer constraint, cannot
// be delegation targets, and must therefore carry an empty (= all-zero)
// frozen-coverage vector.
func evalEnforceFrozenCoverageOnNonDelegationChain(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()
	par.Require(ctx.SelfIsProducedOutput(), "evalEnforceFrozenCoverageOnNonDelegationChain: produced output expected")
	lib := ctx.GetLibrary()
	o := ctx.SelfOutput()

	amounts := o.Amounts()
	cc := o.ChainConstraint()
	par.Require(cc != nil, "evalEnforceFrozenCoverageOnNonDelegationChain: chained output is expected")

	// Read this chain's own sequencer constraint (if any). Absent =>
	// chain is a regular chain — cannot be a delegation target; any
	// non-zero frozen coverage is a structural violation. Present =>
	// chain is a sequencer chain that always accepts delegations with
	// Probe slot 4 for the sequencer constraint. Absent or any other
	// constraint => regular chain (cannot be a delegation target), and a
	// regular chain carries no frozen coverage at all.
	var epochSlots uint32
	var maxFrozenEpochs byte
	if seqBytes, seqErr := o.At(int(SequencerConstraintFixedIndex)); seqErr == nil && len(seqBytes) > 0 {
		if _, err := SequencerConstraintFromBytesWithLib(seqBytes, lib); err == nil {
			epochSlots = lib.DelegationEpochSlots
			maxFrozenEpochs = byte(lib.DelegationMaxFrozenEpochs)
		}
	}

	// produced output
	if cc.IsOrigin() {
		par.Require(amounts.IsFrozenCoverageZero(),
			"evalEnforceFrozenCoverageOnNonDelegationChain: frozen coverage must be 0 on chain origin")
		return par.AllocData(0xff)
	}
	// it is a non-origin chained output
	if maxFrozenEpochs == 0 {
		// chain doesn't accept delegations, so it must carry no frozen coverage
		par.Require(amounts.IsFrozenCoverageZero(),
			"evalEnforceFrozenCoverageOnNonDelegationChain: regular chain (no sequencer constraint) must carry no frozen coverage")
		return par.AllocData(0xff)
	}

	predOut, err := ctx.ConsumedOutput(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	predAmounts := predOut.Amounts()

	path := ctx.EvalPath()

	predID := ctx.MustInputAt(cc.PredecessorInputIndex)
	succID := ctx.OutputID(path[len(path)-2])

	diffEpochsInt := lib.DiffEpochs(cc.ChainID, succID.Timestamp(), predID.Timestamp(), epochSlots)
	par.Require(diffEpochsInt >= 0, "evalEnforceFrozenCoverageOnNonDelegationChain: inconsistency with timestamps")
	diffEpochs := uint32(diffEpochsInt)

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	predecessorFrozenCoverageAdjusted := func(i uint32) (ret int64) {
		if idx := i + diffEpochs; idx < uint32(maxFrozenEpochs) {
			ret = predAmounts.FrozenCoverageAt(byte(idx))
		}
		return
	}

	// Enforce correct frozen coverage on sequencer output.
	// the validity constraint of frozen coverage on the chain at index i:
	// pred_i - value of the predecessor's frozen coverage at index i adjusted for the epoch difference between input and transaction
	// succ_i - value of the successor's (current output) frozen coverage at index i
	// delta_i (aux variable) - sum of frozen coverages (deltas, effectively) of produced delegation outputs at index i (not the target chain)
	// sum_i  - sum of ALL frozen coverages of produced outputs at index i
	// The equations:
	//    pred_i + delta_i = succ_i
	//    succ_i + delta_i = sum_i
	// leads to elimination of delta_i and final enforced validity constraint:
	//    pred_i + sum_i = 2 x succ_i

	for i := 0; i < int(maxFrozenEpochs); i++ {
		successorFrozenCoverage := amounts.FrozenCoverageAt(byte(i))
		predecessorFrozenCoverageValue := predecessorFrozenCoverageAdjusted(uint32(i))
		sum := ctx.ProducedTotal(byte(i) + AmountIndexFrozenCoverage)

		par.Require(2*successorFrozenCoverage == sum+predecessorFrozenCoverageValue,
			"evalEnforceFrozenCoverageOnNonDelegationChain: mismatch between frozen coverage totals at index %d: predCov=%d, succCov=%d, delta=%d, producedSum=%d",
			i, predecessorFrozenCoverageValue, successorFrozenCoverage, successorFrozenCoverage-predecessorFrozenCoverageValue, sum)
	}
	return par.AllocData(0xff)
}
