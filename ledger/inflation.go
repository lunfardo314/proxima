package ledger

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// inflation constraint script, when added to the chain-constrained output, adds inflation the transaction

const inflationConstraintSource = `
// $0 - chain constraint index (sibling)
// $1 - amount 8 bytes
func _checkChainInflation : concat($0, $1)  // TODO

// $0 - randomness proof
func _checkBranchInflationBonus : vrfVerify(
	publicKeyED25519(txSignature),
	$0,
	txTimeSlot
)

// $0 - chain constraint index (sibling)
// $1 - either amount, or randomness proof
func inflation : or(
	selfIsConsumedOutput, // not checked if consumed
	isZero($1),           // zero inflation
	and(
  		selfIsProducedOutput,
		if(
			isBranchTransaction,
			_checkBranchInflationBonus($0),
			_checkChainInflation($0, $1)
		)
    )
)
`

const (
	InflationConstraintName = "inflation"
	// (0) chain constraint index, (1) inflation amount or randomness proof
	inflationConstraintTemplate = InflationConstraintName + "(%d, %s)"
)

type InflationConstraint struct {
	// must point to the sibling chain constraint
	ChainConstraintIndex byte
	AmountOrRndProof     []byte
}

func NewInflationConstraint(chainConstraintIndex byte, amountOrRndProof []byte) *InflationConstraint {
	return &InflationConstraint{
		ChainConstraintIndex: chainConstraintIndex,
		AmountOrRndProof:     amountOrRndProof,
	}
}

func (i *InflationConstraint) Name() string {
	return InflationConstraintName
}

func (i *InflationConstraint) Bytes() []byte {
	return mustBinFromSource(i.source())
}

func (i *InflationConstraint) String() string {
	return i.source()
}

func (i *InflationConstraint) source() string {
	return fmt.Sprintf(inflationConstraintTemplate, i.ChainConstraintIndex, "0x"+hex.EncodeToString(i.AmountOrRndProof))
}

// InflationAmount calculates inflation amount either inside slot, or on branch
func (i *InflationConstraint) InflationAmount(branch bool) uint64 {
	if len(i.AmountOrRndProof) == 0 {
		return 0
	}
	if branch {
		ret := HashAndWrapToUint64(i.AmountOrRndProof)
		return ret % (L().ID.BranchBonusBase + 1)
	}
	if len(i.AmountOrRndProof) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(i.AmountOrRndProof)
}

func InflationConstraintFromBytes(data []byte) (*InflationConstraint, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != InflationConstraintName {
		return nil, fmt.Errorf("not a inflation constraint script")
	}
	cciBin := easyfl.StripDataPrefix(args[0])
	if len(cciBin) != 1 {
		return nil, fmt.Errorf("wrong chainConstraintIndex parameter")
	}
	cci := cciBin[0]

	return &InflationConstraint{
		ChainConstraintIndex: cci,
		AmountOrRndProof:     easyfl.StripDataPrefix(args[1]),
	}, nil
}

func addInflationConstraint(lib *Library) {
	lib.extendWithConstraint(InflationConstraintName, inflationConstraintSource, 2, func(data []byte) (Constraint, error) {
		return InflationConstraintFromBytes(data)
	}, initTestInflationConstraint)
}

func initTestInflationConstraint() {
	data := []byte("123")
	example := NewInflationConstraint(4, data)
	sym, _, args, err := L().ParseBytecodeOneLevel(example.Bytes(), 2)
	util.AssertNoError(err)
	util.Assertf(sym == InflationConstraintName, "sym == InflationConstraintName")

	cciBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(len(cciBin) == 1, "len(cciBin) == 1")
	util.Assertf(cciBin[0] == 4, "cciBin[0] == 4")

	amountBin := easyfl.StripDataPrefix(args[1])
	util.Assertf(bytes.Equal(amountBin, data), "bytes.Equal(amountBin, data)")
}

func HashAndWrapToUint64(data []byte) uint64 {
	h := blake2b.Sum256(data)
	n0 := binary.BigEndian.Uint64(h[0:8])
	n1 := binary.BigEndian.Uint64(h[8:16])
	n2 := binary.BigEndian.Uint64(h[16:24])
	n3 := binary.BigEndian.Uint64(h[24:32])
	return n0 + n1 + n2 + n3
}
