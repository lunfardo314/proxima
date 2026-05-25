package transaction

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set256"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

// IsPartialContext if true, it means consumed UTXOs are not available (yet)
func (tx *Transaction) IsPartialContext() bool {
	return len(tx.MustBytesAtPath(ledger.PathToConsumedOutputs)) == 0
}

// SetFullContext promotes the transaction from partial to full context by loading its
// consumed UTXO bytes via the supplied loader and rebuilding the tuple tree.
//
// Safe to call concurrently and/or multiple times: the setup runs exactly once (guarded
// by a sync.Once on the Transaction); all subsequent or concurrent callers block briefly
// on the Once and then observe the already-populated tree, returning the first call's
// error (if any). The loader from the first winning caller is the one actually invoked —
// this is fine because consumed outputs are determined solely by input OutputIDs, which
// are immutable on the transaction, so all valid loaders produce the same result.
func (tx *Transaction) SetFullContext(inputLoaderByIndex func(i byte) ([]byte, error)) error {
	tx.fullContextOnce.Do(func() {
		tx.fullContextErr = tx.setFullContextLocked(inputLoaderByIndex)
	})
	return tx.fullContextErr
}

// setFullContextLocked is the actual setup. Runs at most once per Transaction, invoked
// from inside fullContextOnce.Do.
func (tx *Transaction) setFullContextLocked(inputLoaderByIndex func(i byte) ([]byte, error)) error {
	consumedUTXOs := tuples.EmptyTupleEditable(256)
	n := tx.NumInputs()
	for i := 0; i < n; i++ {
		b, err := inputLoaderByIndex(byte(i))
		if err != nil {
			return fmt.Errorf("tx.SetFullContext: '%v'", err)
		}
		if b == nil {
			return fmt.Errorf("tx.SetFullContext: cannot get consumed output at input index %d", i)
		}
		consumedUTXOs.MustPush(b)
	}
	txTree, err := tx.Subtree(ledger.PathToRawTransaction)
	util.AssertNoError(err, "tx.Subtree([]byte{ledger.TransactionTuple})")

	// index 0 for transaction, index 1 for consumed outputs
	tx.Tree = tuples.TreeFromTreesReadOnly(txTree, tuples.MakeTupleFromSerializableElements(consumedUTXOs).AsTree())
	util.Assertf(!tx.IsPartialContext(), "tx.SetFullContext: full context expected")

	return nil
}

func (tx *Transaction) SetFullContextWithFetch(fetchOutput func(oid base.OutputID) ([]byte, bool)) error {
	return tx.SetFullContext(func(i byte) ([]byte, error) {
		oid, err := tx.InputAt(i)
		if err != nil {
			return nil, err
		}
		b, ok := fetchOutput(oid)
		if !ok {
			return nil, fmt.Errorf("output %s has not been found", oid.StringShort())
		}
		return b, nil
	})
}

// Bytes return raw transaction bytes
func (tx *Transaction) Bytes() []byte {
	return tx.MustBytesAtPath(ledger.PathToRawTransaction)
}

// ID returns transaction ID
func (tx *Transaction) ID() base.TransactionID {
	return tx.txid
}

// IDString returns human-readable form of the transaction ID
func (tx *Transaction) IDString() string {
	return base.TransactionIDString(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

// IDShortString returns shortened human-readable form of the transaction ID
func (tx *Transaction) IDShortString() string {
	return base.TransactionIDStringShort(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

// IDVeryShortString returns very short human-readable form of the transaction ID
func (tx *Transaction) IDVeryShortString() string {
	return base.TransactionIDStringVeryShort(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

// IDStringHex returns hex encoded bytes of the raw txid bytes
func (tx *Transaction) IDStringHex() string {
	id := tx.ID()
	return id.StringHex()
}

// Slot - slot if the transaction timestamp
func (tx *Transaction) Slot() uint32 {
	return tx.timestamp.Slot
}

func (tx *Transaction) Signature() (*base.Signature, error) {
	sigBytes, err := tx.BytesAtPath(ledger.PathToSignature)
	if err != nil {
		return nil, fmt.Errorf("tx.Signature: %v", err)
	}
	ret, err := base.SignatureFromBytes(sigBytes)
	if err != nil {
		return nil, fmt.Errorf("tx.Signature: %v", err)
	}
	return ret, nil
}

// SequencerTransactionData returns nil it is not a sequencer milestone
func (tx *Transaction) SequencerTransactionData() *ledger.SequencerTransactionData {
	return tx.sequencerTransactionData
}

func (tx *Transaction) ExplicitBaseline() (base.TransactionID, bool) {
	data := tx.MustBytesAtPath(ledger.PathToExplicitBaseline)
	if len(data) == 0 {
		return base.TransactionID{}, false
	}
	ret, err := base.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret, true
}

func (tx *Transaction) IsSequencerTransaction() bool {
	return tx.txid.IsSequencerTransaction()
}

func (tx *Transaction) IsBranchTransaction() bool {
	return tx.txid.IsSequencerTransaction() && tx.timestamp.Tick == 0
}

func (tx *Transaction) StemOutputData() *ledger.StemLock {
	if tx.sequencerTransactionData != nil {
		return tx.sequencerTransactionData.StemOutputData
	}
	return nil
}

func (tx *Transaction) SequencerOutput() *ledger.OutputWithID {
	util.Assertf(tx.IsSequencerTransaction(), "tx.IsSequencerTransaction()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().SequencerOutputIndex)
}

func (tx *Transaction) StemOutput() *ledger.OutputWithID {
	util.Assertf(tx.IsBranchTransaction(), "tx.IsBranchTransaction()")
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) Timestamp() base.LedgerTime {
	return tx.timestamp
}

// Version returns the TxVersion uint16 from the transaction tuple
func (tx *Transaction) Version() uint16 {
	return binary.BigEndian.Uint16(tx.MustBytesAtPath(ledger.PathToTxVersion))
}

func (tx *Transaction) TimestampTime() time.Time {
	return ledger.ClockTime(tx.timestamp)
}

func (tx *Transaction) TotalAmount() uint64 {
	return uint64(tx.producedAmountTotals[0])
}

func (tx *Transaction) NumProducedOutputs() int {
	return tx.MustNumElementsAtPath(ledger.PathToProducedOutputs)
}

func (tx *Transaction) NumInputs() int {
	return tx.MustNumElementsAtPath(ledger.PathToInputIDs)
}

func (tx *Transaction) NumEndorsements() int {
	return tx.MustNumElementsAtPath(ledger.PathToEndorsements)
}

func (tx *Transaction) MustOutputDataAt(idx byte) []byte {
	return tx.MustBytesAtPath(easyfl_util.Concat(ledger.PathToProducedOutputs, idx))
}

func (tx *Transaction) MustProducedOutputAt(idx byte) *ledger.Output {
	ret, err := ledger.OutputFromBytesWithLib(tx.MustOutputDataAt(idx), tx.Library)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputAt(idx byte) (*ledger.Output, error) {
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("ProducedOutputAt: wrong output index %d", idx)
	}
	out, err := ledger.OutputFromBytesWithLib(tx.MustOutputDataAt(idx), tx.Library)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (tx *Transaction) ProducedOutputWithIDAt(idx byte) (*ledger.OutputWithID, error) {
	ret, err := tx.ProducedOutputAt(idx)
	if err != nil {
		return nil, err
	}
	return &ledger.OutputWithID{
		ID:     tx.OutputID(idx),
		Output: ret,
	}, nil
}

func (tx *Transaction) MustProducedOutputWithIDAt(idx byte) *ledger.OutputWithID {
	ret, err := tx.ProducedOutputWithIDAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputs() []*ledger.OutputWithID {
	ret := make([]*ledger.OutputWithID, tx.NumProducedOutputs())
	for i := range ret {
		ret[i] = tx.MustProducedOutputWithIDAt(byte(i))
	}
	return ret
}

func (tx *Transaction) InputAt(idx byte) (ret base.OutputID, err error) {
	if int(idx) >= tx.NumInputs() {
		return [33]byte{}, fmt.Errorf("InputAt: wrong input index")
	}
	ret, err = base.OutputIDFromBytes(tx.MustBytesAtPath(easyfl_util.Concat(ledger.PathToInputIDs, idx)))
	return
}

func (tx *Transaction) MustInputAt(idx byte) base.OutputID {
	ret, err := tx.InputAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) MustOutputIndexOfTheInput(inputIdx byte) byte {
	return base.MustOutputIndexFromIDBytes(tx.MustBytesAtPath(common.Concat(ledger.PathToInputIDs, inputIdx)))
}

func (tx *Transaction) Inputs() []base.OutputID {
	ret := make([]base.OutputID, tx.NumInputs())
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		ret[i] = oid
		return true
	})
	return ret
}

func (tx *Transaction) MustUnlockDataAt(idx byte) []byte {
	return tx.MustBytesAtPath(easyfl_util.Concat(ledger.PathToUnlockParams, idx))
}

func (tx *Transaction) ConsumedOutputAt(idx byte) (ret ledger.OutputWithID, err error) {
	var oid base.OutputID
	if oid, err = tx.InputAt(idx); err != nil {
		return
	}
	var oData []byte
	if oData, err = tx.BytesAtPath(easyfl_util.Concat(ledger.PathToConsumedOutputs, idx)); err != nil {
		return
	}
	var o *ledger.Output
	if o, err = ledger.OutputFromBytes(oData); err != nil {
		return
	}
	ret = ledger.OutputWithID{
		Output: o,
		ID:     oid,
	}
	return
}

func (tx *Transaction) MustEndorsementAt(idx byte) base.TransactionID {
	data := tx.MustBytesAtPath(easyfl_util.Concat(ledger.PathToEndorsements, idx))
	ret, err := base.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) UnlockParameters(inputIdx, constraintIdx byte) ([]byte, error) {
	ret, err := tx.BytesAtPath(easyfl_util.Concat(ledger.PathToUnlockParams, inputIdx, constraintIdx))
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (tx *Transaction) GetLibrary() *ledger.Library {
	return tx.Library
}

func (tx *Transaction) ForEachInputID(fun func(i byte, oid base.OutputID) bool) {
	err := tx.ForEach(func(i byte, data []byte) bool {
		oid, err := base.OutputIDFromBytes(data)
		util.Assertf(err == nil, "ForEachInputID @ %d: %v", i, err)
		return fun(i, oid)
	}, ledger.PathToInputIDs)
	util.AssertNoError(err)
}

func (tx *Transaction) ForEachEndorsement(fun func(idx byte, txid base.TransactionID) bool) {
	err := tx.ForEach(func(i byte, data []byte) bool {
		txid, err := base.TransactionIDFromBytes(data)
		util.Assertf(err == nil, "ForEachEndorsement @ %d: %v", i, err)
		return fun(i, txid)
	}, ledger.PathToEndorsements)
	util.AssertNoError(err)
}

func (tx *Transaction) ForEachProducedOutputData(fun func(idx byte, oData []byte) bool) {
	err := tx.ForEach(func(i byte, data []byte) bool {
		return fun(i, data)
	}, ledger.PathToProducedOutputs)
	util.AssertNoError(err)
}

func (tx *Transaction) ForEachProducedOutput(fun func(idx byte, o *ledger.Output, oid base.OutputID) bool) {
	tx.ForEachProducedOutputData(func(idx byte, oData []byte) bool {
		o, err := ledger.OutputFromBytesWithLib(oData, tx.Library)
		util.AssertNoError(err)
		oid := tx.OutputID(idx)
		if !fun(idx, o, oid) {
			return false
		}
		return true
	})
}

func (tx *Transaction) ForEachConsumedOutput(fun func(idx byte, o ledger.OutputWithID) bool) {
	util.Assertf(!tx.IsPartialContext(), "ForEachConsumedOutput: full context expected")
	n := tx.NumInputs()
	for i := 0; i < n; i++ {
		o, err := tx.ConsumedOutputAt(byte(i))
		util.AssertNoError(err)
		if !fun(byte(i), o) {
			return
		}
	}
}

func (tx *Transaction) OutputID(idx byte) base.OutputID {
	return base.MustNewOutputID(tx.ID(), idx)
}

func (tx *Transaction) InflationAmount() uint64 {
	return uint64(tx.producedAmountTotals[ledger.AmountIndexInflation])
}

func OutputWithIDFromTransactionBytes(txBytes []byte, idx byte) (*ledger.OutputWithID, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return nil, err
	}
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	return tx.ProducedOutputWithIDAt(idx)
}

func OutputsWithIDFromTransactionBytes(txBytes []byte) ([]*ledger.OutputWithID, error) {
	tx, err := Parse(txBytes)
	if err != nil {
		return nil, err
	}

	ret := make([]*ledger.OutputWithID, tx.NumProducedOutputs())
	n := tx.NumProducedOutputs()
	for idx := 0; idx < n; idx++ {
		ret[idx], err = tx.ProducedOutputWithIDAt(byte(idx))
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (tx *Transaction) InputLoaderByIndex(fetchOutput func(oid base.OutputID) ([]byte, bool)) func(byte) ([]byte, error) {
	return func(idx byte) ([]byte, error) {
		inp := tx.MustInputAt(idx)
		odata, ok := fetchOutput(inp)
		if !ok {
			return nil, fmt.Errorf("can't load input #%d: %s", idx, inp.String())
		}
		return odata, nil
	}
}

func (tx *Transaction) InputLoaderFromState(rdr multistate.StateReader) func(idx byte) ([]byte, error) {
	return tx.InputLoaderByIndex(func(oid base.OutputID) ([]byte, bool) {
		return rdr.GetUTXO(oid)
	})
}

func (tx *Transaction) SequencerAndStemInputData() (seqInputIdx *byte, stemInputIdx *byte, seqID *base.ChainID) {
	if !tx.IsSequencerTransaction() {
		return
	}
	seqMeta := tx.SequencerTransactionData()
	if !seqMeta.SequencerOutputData.ChainConstraint.IsOrigin() {
		seqInputIdx = util.Ref(seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
	}
	seqID = util.Ref(seqMeta.SequencerID)

	if tx.IsBranchTransaction() {
		tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
			if oid == seqMeta.StemOutputData.PredecessorOutputID {
				stemInputIdx = util.Ref(i)
			}
			return true
		})
	}
	return
}

// SequencerChainPredecessor returns chain predecessor output ID
// If it is chain origin, it returns nil. Otherwise, it may or may not be a sequencer ID
// It also returns index of the inout
func (tx *Transaction) SequencerChainPredecessor() (base.OutputID, byte) {
	seqMeta := tx.SequencerTransactionData()
	util.Assertf(seqMeta != nil, "SequencerChainPredecessor: must be a sequencer transaction")

	if seqMeta.SequencerOutputData.ChainConstraint.IsOrigin() {
		return base.OutputID{}, 0xff
	}
	ret, err := tx.InputAt(seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex)
	util.AssertNoError(err)
	// The following is ensured by the 'chain' and 'sequencer' constraints on the transaction
	// Returned predecessor outputID must be:
	// - if the transaction is branch tx, then it returns tx id which may or may not be a sequencer transaction id
	// - if the transaction is not a branch tx, it must always return sequencer tx id (which may or may not be a branch)
	return ret, seqMeta.SequencerOutputData.ChainConstraint.PredecessorInputIndex
}

func (tx *Transaction) FindChainOutput(chainID base.ChainID) *ledger.OutputWithID {
	var ret *ledger.OutputWithID
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		cc := o.ChainConstraint()
		if cc == nil {
			return true
		}
		cID := cc.ChainID
		if cc.IsOrigin() {
			cID = base.MakeOriginChainID(oid)
		}
		if cID == chainID {
			ret = &ledger.OutputWithID{
				ID:     oid,
				Output: o,
			}
			return false
		}
		return true
	})
	return ret
}

func (tx *Transaction) FindStemProducedOutput() *ledger.OutputWithID {
	if !tx.IsBranchTransaction() {
		return nil
	}
	return tx.MustProducedOutputWithIDAt(tx.SequencerTransactionData().StemOutputIndex)
}

func (tx *Transaction) StateMutations() *multistate.Mutations {
	ret := multistate.NewMutations()
	tx.ForEachInputID(func(i byte, oid base.OutputID) bool {
		ret.InsertDelOutputMutation(oid)
		return true
	})
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		ret.InsertAddOutputMutation(oid, o)
		return true
	})
	var unspent set256.Set256
	unspent.InsertRange(0, byte(tx.NumProducedOutputs()-1))
	ret.InsertAddTxMutation(tx.ID(), unspent)

	// TODO not correct. ChainIDs of discontinued chains must be deleted. We leave it as is because tx.StateMutations is not used
	//  in the UTXO tangle but mostly in tests
	return ret
}

// BaselineDirection is the input, endorsement or explicit baseline of the sequencer transaction where to look for a baseline branch
// It is not a baseline yet, (but it can be one).
//
// Sequencer chain origins (idx == 0xff) have no chain predecessor;
// _noChainPredecessorCase in def/sequencer.easyfl already requires
// such txs to be non-branch and to carry at least one endorsement. The
// endorsement is therefore the canonical baseline direction.
func (tx *Transaction) BaselineDirection() (ret base.TransactionID) {
	util.Assertf(tx.IsSequencerTransaction(), "tx.IsSequencerTransaction()")
	var ok bool
	if ret, ok = tx.ExplicitBaseline(); ok {
		return
	}
	predOid, idx := tx.SequencerChainPredecessor()
	if idx == 0xff {
		// Sequencer chain origin — no chain predecessor. The EasyFL
		// sequencer constraint guarantees at least one endorsement on
		// this path; use it.
		util.Assertf(tx.NumEndorsements() > 0, "sequencer chain origin must have endorsements\n>>>>>>>>>>>>>>>>>>\n%s", tx.String())
		ret = tx.MustEndorsementAt(0)
		return
	}

	if predOid.Slot() == tx.Slot() {
		if predOid.IsSequencerTransaction() {
			// predecessor is a sequencer in the same-slot
			ret = predOid.TransactionID()
			return
		}
	}
	// the predecessor is cross-slot, or it is not a sequencer transaction.
	if tx.IsBranchTransaction() {
		// for branch transactions, the baseline direction is the predecessor
		ret = predOid.TransactionID()
		return
	}
	// it enforces at least one endorsement
	util.Assertf(tx.NumEndorsements() > 0, "tx.NumEndorsements()>0\n>>>>>>>>>>>>>>>>>>\n%s", tx.String())
	ret = tx.MustEndorsementAt(0)
	return
}

func (tx *Transaction) TotalProducedAmounts() []int64 {
	return tx.producedAmountTotals
}

func (tx *Transaction) InputCommitment() []byte {
	return tx.MustBytesAtPath(ledger.PathToInputCommitment)
}

func (tx *Transaction) AttachmentCost() int {
	return tx.NumInputs() + tx.NumProducedOutputs()
}

// ConsumedOutputHash is ias blake2b hash of the tuple composed of output data
func (tx *Transaction) ConsumedOutputHash() [32]byte {
	util.Assertf(!tx.IsPartialContext(), "ConsumedOutputHash: can't be ekeleton context")
	return blake2b.Sum256(tx.MustBytesAtPath(ledger.PathToConsumedOutputs))
}

func (tx *Transaction) BytesAtPath(path []byte) ([]byte, error) {
	return tx.Tree.BytesAtPath(path)
}

func (tx *Transaction) ConsumedOutput(idx byte) (*ledger.Output, error) {
	ret, err := tx.ConsumedOutputAt(idx)
	if err != nil {
		return nil, err
	}
	return ret.Output, nil
}

func (tx *Transaction) ConsumedTotal(i byte) (ret int64) {
	if i == 0 {
		return tx.totalConsumedTokenBalance
	}
	tx.ForEachConsumedOutput(func(idx byte, o ledger.OutputWithID) bool {
		ret += o.Amounts().Amount(i)
		return true
	})
	return
}

func (tx *Transaction) ProducedTotal(i byte) int64 {
	util.Assertf(int(i) < len(tx.producedAmountTotals), "ProducedTotal: wrong index %d", i)
	return tx.producedAmountTotals[i]
}

func (tx *Transaction) HolderID() (base.HolderID, error) {
	sig, err := tx.Signature()
	if err != nil {
		return [32]byte{}, err
	}
	return sig.HolderID(), nil
}

// HasOutputForSequencer returns true if any produced output has a lock targeting
// the given sequencer chain ID (chainLock, tagAlong, or delegateLock).
func (tx *Transaction) HasOutputForSequencer(seqID base.ChainID) bool {
	found := false
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, _ base.OutputID) bool {
		lock := o.Lock()
		switch l := lock.(type) {
		case ledger.ChainLock:
			if l.ChainID() == seqID {
				found = true
				return false
			}
		case *ledger.TagAlongLock:
			if l.TargetSequencerID == seqID {
				found = true
				return false
			}
		case *ledger.DelegateLock:
			if l.Target == seqID {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
