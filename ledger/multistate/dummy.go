package multistate

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/unitrie/common"
)

type _dummyStateReader struct{}

var DummyStateReader = _dummyStateReader{}

func (d _dummyStateReader) GetUTXO(id base.OutputID) ([]byte, bool) {
	return nil, false
}

func (d _dummyStateReader) HasUTXO(id base.OutputID) bool {
	return false
}

func (d _dummyStateReader) KnowsCommittedTransaction(txid base.TransactionID) bool {
	return false
}

func (d _dummyStateReader) IterateUTXOIDsForController(addr ledger.ControllerID, fun func(oid base.OutputID) bool) (err error) {
	return nil
}

func (d _dummyStateReader) IterateUTXOsForController(addr ledger.ControllerID, fun func(oid base.OutputID, odata []byte) bool) (err error) {
	return nil
}

func (d _dummyStateReader) IterateUTXOsInSlotChunk(chunk uint32, fun func(oid base.OutputID, oData []byte) bool) (err error) {
	return
}

func (d _dummyStateReader) IterateUTXOsInSlot(slot uint32, fun func(oid base.OutputID, oData []byte) bool) (err error) {
	return nil
}

func (d _dummyStateReader) IterateUTXOs(fun func(o ledger.OutputWithID) bool) (err error) {
	return nil
}

func (d _dummyStateReader) IterateChainTips(fun func(chainID base.ChainID, oid base.OutputID) bool) error {
	return nil
}

func (d _dummyStateReader) GetUTXOIDsForController(addr ledger.ControllerID) ([]base.OutputID, error) {
	return nil, nil
}

func (d _dummyStateReader) GetUTXOsForController(accountID ledger.ControllerID) ([]*ledger.OutputDataWithID, error) {
	return nil, nil
}

func (d _dummyStateReader) GetUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error) {
	return nil, nil
}

func (d _dummyStateReader) Root() common.VCommitment {
	return nil
}

func (d _dummyStateReader) MustLedgerIdentityBytes() []byte {
	return nil
}

func (d _dummyStateReader) IsKnownController(accountID ledger.ControllerID) bool {
	return true
}
