package core

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// the RoyaltiesED25519 constraint forces sending specified amount of tokens to specified address

type RoyaltiesED25519 struct {
	Address AddressED25519
	Amount  uint64
}

const (
	RoyaltiesED25519Name     = "royaltiesED25519"
	royaltiesED25519Template = RoyaltiesED25519Name + "(0x%s, u64/%d)"
)

func NewRoyalties(addr AddressED25519, amount uint64) *RoyaltiesED25519 {
	return &RoyaltiesED25519{
		Address: addr,
		Amount:  amount,
	}
}

func RoyaltiesED25519FromBytes(data []byte) (*RoyaltiesED25519, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != RoyaltiesED25519Name {
		return nil, fmt.Errorf("not a royaltiesED25519")
	}
	addrBin := easyfl.StripDataPrefix(args[0])
	addr, err := AddressED25519FromBytes(addrBin)
	if err != nil {
		return nil, err
	}
	amountBin := easyfl.StripDataPrefix(args[1])
	if len(amountBin) != 8 {
		return nil, fmt.Errorf("wrong amount")
	}
	return NewRoyalties(addr, binary.BigEndian.Uint64(amountBin)), nil
}

func (cl *RoyaltiesED25519) source() string {
	return fmt.Sprintf(royaltiesED25519Template, hex.EncodeToString(cl.Address.Bytes()), cl.Amount)
}

func (cl *RoyaltiesED25519) Bytes() []byte {
	return mustBinFromSource(cl.source())
}

func (cl *RoyaltiesED25519) Name() string {
	return RoyaltiesED25519Name
}

func (cl RoyaltiesED25519) String() string {
	return cl.source()
}

func initRoyaltiesED25519Constraint() {
	easyfl.MustExtendMany(RoyaltiesED25519Source)

	addr0 := AddressED25519Null()
	example := NewRoyalties(addr0, 1337)
	royaltiesBack, err := RoyaltiesED25519FromBytes(example.Bytes())
	util.AssertNoError(err)
	util.Assertf(EqualConstraints(royaltiesBack.Address, addr0), "inconsistency "+RoyaltiesED25519Name)
	util.Assertf(royaltiesBack.Amount == 1337, "inconsistency "+RoyaltiesED25519Name)

	prefix, err := easyfl.ParseBytecodePrefix(example.Bytes())
	util.AssertNoError(err)

	registerConstraint(RoyaltiesED25519Name, prefix, func(data []byte) (Constraint, error) {
		return RoyaltiesED25519FromBytes(data)
	})
}

const RoyaltiesED25519Source = `
// constraint royaltiesED25519($0, $1) enforces sending at least amount $1 to the address $0 
// The 1-byte long unlock parameters of the constraint must point to the output which sends at least specified amount of 
// tokens to the lock constraint specified by $0

func royaltiesED25519 : or(
	selfIsProducedOutput,  // the constrain is always satisfied on 'produced' side'
	and(
		selfIsConsumedOutput,
		equal(
			$0,
			lockConstraint(producedOutputByIndex(selfUnlockParameters))
		),
		lessOrEqualThan(
			$1,
			amountValue(producedOutputByIndex(selfUnlockParameters))
		)
	),
	!!!royaltiesED25519_constraint_failed
)
`
