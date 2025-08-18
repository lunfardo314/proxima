package txbuilder_seq

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"golang.org/x/crypto/blake2b"
)

type SetSequencerDataCommand struct {
	SequencerCommandBase
	seqdata.SequencerData
}

const (
	SetSequencerDataCmdCode = byte(2)

	FieldSetSequencerDataBinary = byte(1)
)

func init() {
	registerSequencerCommand(SetSequencerDataCmdCode, func(cmdBase SequencerCommandBase) (SequencerCommand, bool) {
		sd, err := seqdata.FromBytes(cmdBase.Get(FieldSetSequencerDataBinary))
		if err != nil {
			return nil, false
		}
		return &SetSequencerDataCommand{
			SequencerCommandBase: cmdBase,
			SequencerData:        sd,
		}, true
	})
}

func NewSetSequencerDataCommandBytecode(privKey ed25519.PrivateKey, seqData *seqdata.SequencerData) []byte {
	body := base.NewSmallPersistentMap()
	body.Set(FieldCmdCode, []byte{SetSequencerDataCmdCode})
	body.Set(FieldSetSequencerDataBinary, seqData.Bytes())

	msg := ledger.NewMessageWithED25519SenderFromPrivateKey(privKey, body.Bytes())
	return msg.Bytes()
}

func NewSeqDataCommandOutput(targetChain base.ChainID, privKey ed25519.PrivateKey, fee uint64, seqData *seqdata.SequencerData) *ledger.Output {
	return ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithTokenBalance(fee).WithLock(ledger.ChainLockFromChainID(targetChain))
		o.MustPushConstraint(NewSetSequencerDataCommandBytecode(privKey, seqData))
	})
}

func (cmd *SetSequencerDataCommand) CheckPreconditions(txb *SequencerTxBuilder) (bool, bool, int) {
	pubKey := txb.privateKey.Public().(ed25519.PublicKey)
	return cmd.MessageWithED25519Sender.SenderHash == blake2b.Sum256(pubKey), true, 0
}

func (cmd *SetSequencerDataCommand) Apply(txb *SequencerTxBuilder) {
	txb.nextSeqData = cmd.SequencerData.Clone()
	return
}
