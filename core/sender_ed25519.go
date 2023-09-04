package core

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type SenderAddressED25519 struct {
	Address AddressED25519
}

const (
	SenderAddressED25519Name     = "senderAddressED25519"
	senderAddressED25519Template = SenderAddressED25519Name + "(0x%s)"
)

func NewSenderAddressED25519(addr AddressED25519) *SenderAddressED25519 {
	return &SenderAddressED25519{Address: addr}
}

func (s *SenderAddressED25519) Name() string {
	return SenderAddressED25519Name
}

func (s *SenderAddressED25519) Bytes() []byte {
	return mustBinFromSource(s.source())
}

func (s *SenderAddressED25519) String() string {
	return fmt.Sprintf("%s(%s)", SenderAddressED25519Name, easyfl.Fmt(s.Address))
}

func (s *SenderAddressED25519) source() string {
	return fmt.Sprintf(senderAddressED25519Template, hex.EncodeToString(s.Address))
}

func SenderAddressED25519FromBytes(data []byte) (*SenderAddressED25519, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != SenderAddressED25519Name {
		return nil, fmt.Errorf("not a SenderAddressED25519 constraint")
	}
	addr := AddressED25519(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}
	return &SenderAddressED25519{addr}, nil
}

func initSenderConstraint() {
	easyfl.MustExtendMany(senderAddressED25519Source)

	addr := AddressED25519Null()
	example := NewSenderAddressED25519(addr)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	addrBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == SenderAddressED25519Name && bytes.Equal(addrBin, addr), "inconsistency in 'senderAddressED25519'")

	registerConstraint(SenderAddressED25519Name, prefix, func(data []byte) (Constraint, error) {
		return SenderAddressED25519FromBytes(data)
	})
}

const senderAddressED25519Source = `
// $0 - address 
func senderAddressED25519: or(
	selfIsConsumedOutput,
	and(
		selfIsProducedOutput,
		equal(
       		$0, 
			blake2b(publicKeyED25519(txSignature))
		)
	)
)
`
