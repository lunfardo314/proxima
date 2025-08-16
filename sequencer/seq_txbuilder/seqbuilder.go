package seq_txbuilder

import (
	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
)

type SequencerTxBuilder struct {
	*txbuilder.TxBuilder
	privateKey ed25519.PrivateKey
	timestamp  base.LedgerTime
	chainInput *ledger.OutputWithChainID
	stemInput  *ledger.OutputWithID // it is branch tx if != nil
}

func New(ts base.LedgerTime, predecessor *ledger.OutputWithChainID, stem *ledger.OutputWithID, privateKey ed25519.PrivateKey) *SequencerTxBuilder {
	return &SequencerTxBuilder{
		privateKey: privateKey,
		timestamp:  ts,
		chainInput: predecessor,
		stemInput:  stem,
		TxBuilder:  txbuilder.New(),
	}
}

func (txb *SequencerTxBuilder) AddEndorsement(txid base.TransactionID) error {
	panic("implement me")
}

func (txb *SequencerTxBuilder) AddExplicitBaseline(txid base.TransactionID) error {
	panic("implement me")
}

func (txb *SequencerTxBuilder) AddTagAlongInput(out *ledger.OutputWithID) error {
	panic("implement me")
}

func (txb *SequencerTxBuilder) AddDelegationInput(out *ledger.DelegateOutput) error {
	panic("implement me")
}
