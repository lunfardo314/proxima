package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

type SenderED25519 struct {
	Address AddressED25519
}

const senderED25519Source = `
// Enforces valid sender check in the output. It means, the ledger guarantees that the ED25511 address
// data in the output is the one which signed the transaction which produced it.
// $0 - address bytes without constraint prefix, i.e. blake2b hash of the public key 
func senderED25519: or(
    // always valid on consumed output
	selfIsConsumedOutput,
    // valid on produced output only if public key of the signature in the transaction 
    // corresponds to the address bytes
	and(
		selfIsProducedOutput,
		equal(
       		$0, 
			blake2b(publicKeyED25519(txSignature))
		)
	)
)
`

const (
	SenderAddressED25519Name = "senderED25519"
	senderED25519Template    = SenderAddressED25519Name + "(0x%s)"
)

func NewSenderED25519(addr AddressED25519) *SenderED25519 {
	return &SenderED25519{Address: addr}
}

func (s *SenderED25519) Name() string {
	return SenderAddressED25519Name
}

func (s *SenderED25519) Bytes() []byte {
	return mustBinFromSource(s.source())
}

func (s *SenderED25519) String() string {
	return fmt.Sprintf("%s(%s)", SenderAddressED25519Name, easyfl.Fmt(s.Address))
}

func (s *SenderED25519) source() string {
	return fmt.Sprintf(senderED25519Template, hex.EncodeToString(s.Address))
}

func SenderED25519FromBytes(data []byte) (*SenderED25519, error) {
	sym, _, args, err := easyfl.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, err
	}
	if sym != SenderAddressED25519Name {
		return nil, fmt.Errorf("not a SenderED25519 constraint")
	}
	addr := AddressED25519(easyfl.StripDataPrefix(args[0]))
	if err != nil {
		return nil, err
	}
	return &SenderED25519{addr}, nil
}

func initSenderED25519Constraint() {
	easyfl.MustExtendMany(senderED25519Source)

	addr := AddressED25519Null()
	example := NewSenderED25519(addr)
	sym, prefix, args, err := easyfl.ParseBytecodeOneLevel(example.Bytes(), 1)
	util.AssertNoError(err)
	addrBin := easyfl.StripDataPrefix(args[0])
	util.Assertf(sym == SenderAddressED25519Name && bytes.Equal(addrBin, addr), "inconsistency in 'senderAddressED25519'")

	registerConstraint(SenderAddressED25519Name, prefix, func(data []byte) (Constraint, error) {
		return SenderED25519FromBytes(data)
	})
}
