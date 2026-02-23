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
	// amount on the chain at the origin
	OriginAmount uint64
}

const (
	ChainConstraintName     = "chain"
	chainConstraintTemplate = ChainConstraintName + "(0x%s, 0x%s, z32/%d, z64/%d)"
)

func NewChainConstraint(id base.ChainID, predInputIndex byte, originSlot uint32, originAmount uint64) *ChainConstraint {
	return &ChainConstraint{
		ChainID:               id,
		PredecessorInputIndex: predInputIndex,
		OriginSlot:            originSlot,
		OriginAmount:          originAmount,
	}
}

func NewChainOrigin(startSlot uint32, startAmount uint64) *ChainConstraint {
	return NewChainConstraint(base.NilChainID, 0xff, startSlot, startAmount)
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
	return fmt.Sprintf("%s(%s, predInputIdx=%s, originSlot=%d, originAmount=%s)",
		ChainConstraintName, chID, predRefStr, cc.OriginSlot, util.Th(cc.OriginAmount))
}

func (cc *ChainConstraint) Source() string {
	var predRefHex string
	if !cc.IsOrigin() {
		predRefHex = hex.EncodeToString([]byte{cc.PredecessorInputIndex})
	}
	// For origin, predRefHex is empty → "0x" in EasyFL (empty bytes)
	return fmt.Sprintf(chainConstraintTemplate,
		hex.EncodeToString(cc.ChainID[:]), predRefHex, cc.OriginSlot, cc.OriginAmount)
}

func ChainConstraintFromBytes(data []byte) (*ChainConstraint, error) {
	return ChainConstraintFromBytesWithLib(data, L(base.MaxSlot))
}

func ChainConstraintFromBytesWithLib(data []byte, lib *Library) (*ChainConstraint, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 4)
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
	if ret.OriginAmount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[3])); err != nil {
		return nil, err
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
	lib.mustRegisterConstraint(ChainConstraintName, 4, func(data []byte) (Constraint, error) {
		// Use latest library version for library registration parsing
		return ChainConstraintFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		example := NewChainOrigin(1000, 10_000_000)
		// Use latest library version for test
		back, err := ChainConstraintFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(back.Bytes(), example.Bytes()), "inconsistency in "+ChainConstraintName)
		util.Assertf(back.OriginSlot == 1000, "back.OriginSlot == 1000")
		util.Assertf(back.OriginAmount == 10_000_000, "back.OriginAmount == 10_000_000")

		var chainID base.ChainID
		chainID = blake2b.Sum256([]byte("dummy"))
		{
			chainIDBack, err := base.ChainIDFromBytes(chainID.Bytes())
			util.AssertNoError(err)
			util.Assertf(chainIDBack == chainID, "chainIDBack == chainID")
		}
		{
			chainConstr := NewChainConstraint(chainID, 0, 1000, 10_000_000)
			chainConstrBack, err := ChainConstraintFromBytesWithLib(chainConstr.Bytes(), lib)
			util.AssertNoError(err)
			util.Assertf(*chainConstrBack == *chainConstr, "*chainConstrBack == *chainConstr")
		}
	})
}

// evalEnforceFrozenCoverageOnNonDelegationChain assumes sequencer output and enforces the validity of the frozen coverage values
func evalEnforceFrozenCoverageOnNonDelegationChain(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()
	par.Require(ctx.SelfIsProducedOutput(), "evalEnforceFrozenCoverageOnNonDelegationChain: produced output expected")
	lib := ctx.GetLibrary()
	o := ctx.SelfOutput()

	amounts := o.Amounts()
	cc := o.ChainConstraint()
	par.Require(cc != nil, "evalEnforceFrozenCoverageOnNonDelegationChain: chained output is expected")
	// produced output
	if cc.IsOrigin() {
		par.Require(amounts.IsFrozenCoverageZero(byte(lib.MaxFrozenEpochs)), "evalEnforceFrozenCoverageOnNonDelegationChain: frozen coverage must be 0 on chain origin")
		return par.AllocData(0xff)
	}
	// it is a non-origin chained output

	predOut, err := ctx.ConsumedOutput(cc.PredecessorInputIndex)
	par.RequireNoError(err)
	predAmounts := predOut.Amounts()

	path := ctx.EvalPath()

	predID := ctx.MustInputAt(cc.PredecessorInputIndex)
	succID := ctx.OutputID(path[len(path)-2])

	diffEpochsInt := lib.DiffEpochs(cc.ChainID, succID.Timestamp(), predID.Timestamp())
	par.Require(diffEpochsInt >= 0, "evalEnforceFrozenCoverageOnNonDelegationChain: inconsistency with timestamps")
	diffEpochs := uint32(diffEpochsInt)

	// frozen coverage at the predecessor adjusted to the epoch of the successor
	predecessorFrozenCoverageAdjusted := func(i uint32) (ret int64) {
		if idx := i + diffEpochs; idx < lib.MaxFrozenEpochs {
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

	for i := 0; i < int(lib.MaxFrozenEpochs); i++ {
		successorFrozenCoverage := amounts.FrozenCoverageAt(byte(i))
		predecessorFrozenCoverageValue := predecessorFrozenCoverageAdjusted(uint32(i))
		sum := ctx.ProducedTotal(byte(i + 2))

		par.Require(2*successorFrozenCoverage == sum+predecessorFrozenCoverageValue,
			"evalEnforceFrozenCoverageOnNonDelegationChain: mismatch between frozen coverage totals at index %d: predCov=%d, succCov=%d, delta=%d, producedSum=%d",
			i, predecessorFrozenCoverageValue, successorFrozenCoverage, successorFrozenCoverage-predecessorFrozenCoverageValue, sum)
	}
	return par.AllocData(0xff)
}
