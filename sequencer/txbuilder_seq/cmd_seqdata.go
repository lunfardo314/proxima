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

func (cmd *SetSequencerDataCommand) CheckPreconditions(txb *SequencerTxBuilder) (bool, bool) {
	pubKey := txb.privateKey.Public().(ed25519.PublicKey)
	return cmd.MessageWithED25519Sender.SenderHash == blake2b.Sum256(pubKey), true
}

func (cmd *SetSequencerDataCommand) Apply(txb *SequencerTxBuilder) {
	txb.nextSeqData = cmd.SequencerData.Clone()
	return
}

func (cmd *SetSequencerDataCommand) ProducesAdditionalOutputs() int {
	return 0
}
