package txbuilder_seq

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"golang.org/x/crypto/blake2b"
)

type SetSequencerDataCommand struct {
	o ledger.OutputWithID
	seqdata.SequencerData
}

const (
	SetSequencerDataCmdCode = byte(2)

	FieldSetSequencerDataBinary = byte(1)
)

func init() {
	registerSequencerCommand(SetSequencerDataCmdCode, _parseSetSequencerDataOutput)
}

func _parseSetSequencerDataOutput(txb *SeqTxBuilder, o ledger.OutputWithID, msg *SeqCommandMessage) (cmd TxBuilderCommand, isValid bool) {
	if o.Output.NumConstraints() != 3 {
		// unexpected structure -> may be attack
		return
	}
	// authenticate
	publicKey := txb.privateKey.Public().(ed25519.PublicKey)
	if msg.SenderHash != blake2b.Sum256(publicKey) {
		// wrong sender -> may be attack
		return
	}
	sd, err := seqdata.FromBytes(msg.Get(FieldSetSequencerDataBinary))
	if err != nil {
		return
	}
	return &SetSequencerDataCommand{o: o, SequencerData: sd}, true
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

func (cmd *SetSequencerDataCommand) Apply(txb *SeqTxBuilder) error {
	_, err := txb.ConsumeTagAlongOutputUnlock(cmd.o.Output, cmd.o.ID, 0, txb.chainInput.ChainConstraintIndex)
	if err != nil {
		return err
	}
	txb.chainOutAmounts[ledger.AmountIndexTokenBalance] += int64(cmd.o.Output.TokenBalance())
	txb.nextSeqData = cmd.SequencerData.Clone()
	return nil
}
