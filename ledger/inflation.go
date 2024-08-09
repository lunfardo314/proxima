package ledger

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// Inflation constraint script, when added to the chain-constrained output, adds inflation the transaction.
// It enforces
// - valid chain inflation value (proportional to capital and time)
// - valid branch inflation randomness proof (as per VRF) for branches
// The total inflation value is enforced at the transaction level.
// Inflation on the output equals to:
// - 0 if 'inflation' constraint script is not present
// - for branches, it is calculated from the branch inflation randomness proof. See BranchInflationBonusFromRandomnessProof
// - for non-branches:
//   -- which has branch as a predecessor sums up delayed chain inflation on the branch plus own chain inflation
//   -- for other non-branches it is equal to the chainInflation
//
// This trick with delayed inflation is necessary to:
// 1. make inflation on the branch random in order to enforce fair selection of branches by the biggest coverage rule
// 2. not to lose chain inflation when passing to another slot with the winning branch. For that, the chain inflation
// is calculated for the branch but used in the next, successor, transaction by adding delayed inflation

const (
	InflationConstraintName = "inflation"
	// (0) chain constraint index, (1) inflation amount or randomness proof
	inflationConstraintTemplate = InflationConstraintName + "(%s, %s, %d, %s)"
)

type InflationConstraint struct {
	// ChainInflation inflation amount calculated according to chain inflation rule. It is used inside slot and delayed on slot boundary
	// and can be added to the inflation of the next transaction in the chain
	ChainInflation uint64
	// VRFProof VRF randomness proof, used to proof VRF and calculate inflation amount on branch
	// nil for non-branch transactions
	VRFProof []byte
	// ChainConstraintIndex must point to the sibling chain constraint
	ChainConstraintIndex byte
	// DelayedInflationIndex
	// Used only if branch successor to enforce correct ChainInflation which will sum of delayed inflation and current inflation
	// If not used, must be 0xff
	DelayedInflationIndex byte
}

func (i *InflationConstraint) Name() string {
	return InflationConstraintName
}

func (i *InflationConstraint) Bytes() []byte {
	return mustBinFromSource(i.source())
}

func (i *InflationConstraint) String() string {
	return fmt.Sprintf("%s(%s, 0x%s, %d, %d)",
		InflationConstraintName,
		util.Th(i.ChainInflation),
		hex.EncodeToString(i.VRFProof),
		i.ChainConstraintIndex,
		i.DelayedInflationIndex,
	)
}

func (i *InflationConstraint) source() string {
	var chainInflationBin [8]byte
	binary.BigEndian.PutUint64(chainInflationBin[:], i.ChainInflation)
	chainInflationStr := "0x" + hex.EncodeToString(chainInflationBin[:])

	vrfProofStr := "0x" + hex.EncodeToString(i.VRFProof)
	delayedInflationIndexStr := "0xff"
	if i.DelayedInflationIndex != 0xff {
		delayedInflationIndexStr = fmt.Sprintf("%d", i.DelayedInflationIndex)
	}
	return fmt.Sprintf(inflationConstraintTemplate, chainInflationStr, vrfProofStr, i.ChainConstraintIndex, delayedInflationIndexStr)
}

// InflationAmount calculates inflation amount either inside slot, or on the slot boundary
func (i *InflationConstraint) InflationAmount(slotBoundary bool) uint64 {
	if slotBoundary {
		// the ChainInflation is interpreted as delayed inflation
		return L().BranchInflationBonusFromRandomnessProof(i.VRFProof)
	}
	return i.ChainInflation
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 4)
	if err != nil {
		return nil, err
	}
	if sym != InflationConstraintName {
		return nil, fmt.Errorf("InflationConstraintFromBytes: not a inflation constraint script")
	}
	var amount uint64
	amountBin := easyfl.StripDataPrefix(args[0])
	if len(amountBin) != 8 {
		return nil, fmt.Errorf("InflationConstraintFromBytes: wrong chainInflation parameter")
	}
	amount = binary.BigEndian.Uint64(amountBin)

	vrfProof := easyfl.StripDataPrefix(args[1])

	cciBin := easyfl.StripDataPrefix(args[2])
	if len(cciBin) != 1 {
		return nil, fmt.Errorf("InflationConstraintFromBytes: wrong chainConstraintIndex parameter")
	}
	cci := cciBin[0]

	delayedInflationIndex := byte(0xff)
	idxBin := easyfl.StripDataPrefix(args[3])

	switch {
	case len(idxBin) == 1:
		delayedInflationIndex = idxBin[0]
	case len(idxBin) > 1:
		return nil, fmt.Errorf("InflationConstraintFromBytes: wrong delayed inflation index parameter")
	}
	return &InflationConstraint{
		ChainConstraintIndex:  cci,
		ChainInflation:        amount,
		VRFProof:              vrfProof,
		DelayedInflationIndex: delayedInflationIndex,
	}, nil
}

func addInflationConstraint(lib *Library) {
	lib.MustExtendMany(inflationFunctionsSource)
	lib.extendWithConstraint(InflationConstraintName, inflationConstraintSource, 4, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	})
}

const inflationConstraintSource = `
// $0 - chain constraint index
// $1 - index with the delayed inflation in the predecessor
// 
// returns:
// - chain inflation in the predecessor branch transaction
// - 0 if delayed inflation not specified or predecessor is not branch
//
func delayedInflationValue : 
if(
	equal($1, 0xff),  
	u64/0,  // delayed inflation index on predecessor is not specified ->  not delayed inflation is zero.
	if(
		isBranchOutputID(inputIDByIndex(chainPredecessorInputIndex($0))),
		// previous is branch -> parse first argument from the inflation constraint there 
		evalArgumentBytecode(
			consumedConstraintByIndex(concat(chainPredecessorInputIndex($0), $1)),
			selfBytecodePrefix,
			0
		),
		// previous is not a branch -> nothing is delayed
		u64/0
	)
)

// inflation(<inflation amount>, <VRF proof>, <chain constraint index>, <delayed inflation index>)
// $0 - chain inflation amount (8 bytes or isZero). On slot boundary interpreted as delayed inflation 
// $1 - vrf proof. Interpreted only on branch transactions
// $2 - chain constraint index (sibling)
// $3 - delayed inflation index. Inflation constraint index in the predecessor, 0xff means not specified
//
func inflation : or(
	selfIsConsumedOutput, // not checked if consumed
	isZero($0),           // zero inflation always ok
	and(
  		selfIsProducedOutput,
		require(equalUint(len($3), 1), !!!delayed_inflation_index_must_be_1_byte),
		require(
			equalUint(
				calcChainInflationAmount(
					timestampOfInputByIndex(chainPredecessorInputIndex($2)), 
					ticksBefore(
                       timestampOfInputByIndex(chainPredecessorInputIndex($2)),
                       txTimestampBytes
                    ), 
					amountValue(consumedOutputByIndex(chainPredecessorInputIndex($2))),
					delayedInflationValue($2, $3)
				),				
				$0
			),
			!!!invalid_chain_inflation_amount
		),
		require(
			or(
				not(isBranchTransaction),
				vrfVerify(publicKeyED25519(txSignature), $1, predStemOutputIDOfSelf)
			),
			!!!VRF_verification_failed
		)
    )
)
`
