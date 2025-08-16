package ledger

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/util"
	"golang.org/x/crypto/blake2b"
)

// MessageWithED25519Sender is a constraint which enforces trust-less sender identity next to arbitrary data in the UTXO
type MessageWithED25519Sender struct {
	SenderHash [32]byte // blake2b hash of the sender public key
	Msg        []byte   // arbitrary data attached. Interpreted by the receiver such as a sequencer
}

const messageWithED25519SenderSource = `
// Contains arbitrary message and enforces valid sender (originator) as part of the message.
// Once output is in the state, it is guaranteed to have the real sender
// $0 - blake2b hash of the signature's public key (not an address, just data)
// $1 - arbitrary data
func msgED25519: or(
    // always valid on consumed output
	selfIsConsumedOutput,
    // valid on produced output only if public key of the signature of the transaction equal to $0
	and(
		selfIsProducedOutput,
		equal(
       		$0, 
			blake2b(publicKeyED25519(txSignature))
		),
        $1 // to enforce mandatory second parameter. It is evaluated
	)
)
`

const (
	MessageWithED25519SenderName     = "msgED25519"
	messageWithED25519SenderTemplate = MessageWithED25519SenderName + "(0x%s,0x%s)"
)

func NewMessageWithED25519SenderFromPublicKey(pubKey ed25519.PublicKey, data []byte) *MessageWithED25519Sender {
	return &MessageWithED25519Sender{
		Msg:        data,
		SenderHash: blake2b.Sum256(pubKey),
	}
}

func NewMessageWithED25519SenderFromAddress(addr AddressED25519, data []byte) *MessageWithED25519Sender {
	ret := &MessageWithED25519Sender{
		Msg: data,
	}
	copy(ret.SenderHash[:], addr)
	return ret
}

var _ Constraint = &MessageWithED25519Sender{}

func (s *MessageWithED25519Sender) Name() string {
	return MessageWithED25519SenderName
}

func (s *MessageWithED25519Sender) Bytes() []byte {
	return mustBinFromSource(s.Source())
}

func (s *MessageWithED25519Sender) String() string {
	return fmt.Sprintf("%s(%s,%s)", MessageWithED25519SenderName, easyfl_util.Fmt(s.SenderHash[:]), easyfl_util.Fmt(s.Msg))
}

func (s *MessageWithED25519Sender) Source() string {
	return fmt.Sprintf(messageWithED25519SenderTemplate, hex.EncodeToString(s.SenderHash[:]), hex.EncodeToString(s.Msg))
}

func MessageWithSenderED25519FromBytes(data []byte) (*MessageWithED25519Sender, error) {
	sym, _, args, err := L().ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, err
	}
	if sym != MessageWithED25519SenderName {
		return nil, fmt.Errorf("not a MessageWithED25519Sender constraint")
	}
	ret := &MessageWithED25519Sender{
		Msg: easyfl.StripDataPrefix(args[1]),
	}
	copy(ret.SenderHash[:], easyfl.StripDataPrefix(args[0]))
	return ret, nil
}

func registerMessageWithSenderED25519Constraint(lib *Library) {
	lib.mustRegisterConstraint(MessageWithED25519SenderName, 2, func(data []byte) (Constraint, error) {
		return MessageWithSenderED25519FromBytes(data)
	}, initTestSenderED25519Constraint)
}

func initTestSenderED25519Constraint() {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	addr := AddressED25519FromPublicKey(pubKey)

	example := NewMessageWithED25519SenderFromPublicKey(pubKey, []byte("12"))
	c, err := ConstraintFromBytes(example.Bytes())
	util.AssertNoError(err)
	cBack, ok := c.(*MessageWithED25519Sender)
	util.Assertf(ok, "inconsistency: MessageWithED25519Sender 1")
	util.Assertf(bytes.Equal(addr, cBack.SenderHash[:]), "inconsistency: MessageWithED25519Sender 2")
	util.Assertf(bytes.Equal([]byte("12"), cBack.Msg), "inconsistency: MessageWithED25519Sender 3")
}
