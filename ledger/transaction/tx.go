package transaction

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/lunfardo314/unitrie/common"
	"golang.org/x/crypto/blake2b"
)

func (tx *Transaction) ID() base.TransactionID {
	return tx.txid
}

func (tx *Transaction) IDString() string {
	return base.TransactionIDString(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

func (tx *Transaction) IDShortString() string {
	return base.TransactionIDStringShort(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

func (tx *Transaction) IDVeryShortString() string {
	return base.TransactionIDStringVeryShort(tx.timestamp, tx.txid.ShortID(), tx.txid.IsSequencerTransaction())
}

func (tx *Transaction) IDStringHex() string {
	id := tx.ID()
	return id.StringHex()
}

func (tx *Transaction) Slot() uint32 {
	return tx.timestamp.Slot
}

func (tx *Transaction) Hash() base.TransactionIDShort {
	return tx.txid.ShortID()
}

func (tx *Transaction) Signature() (*base.Signature, error) {
	sigBytes, err := tx.BytesAtPath(Path(ledger.TxSignatureData))
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
	data := tx.MustBytesAtPath(Path(ledger.TxExplicitBaseline))
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

func (tx *Transaction) TimestampTime() time.Time {
	return ledger.ClockTime(tx.timestamp)
}

func (tx *Transaction) TotalAmount() uint64 {
	return uint64(tx.producedAmountTotals[0])
}

func (tx *Transaction) NumProducedOutputs() int {
	return tx.MustNumElementsAtPath(Path(ledger.TxOutputs))
}

func (tx *Transaction) NumInputs() int {
	return tx.MustNumElementsAtPath(Path(ledger.TxInputIDs))
}

func (tx *Transaction) NumEndorsements() int {
	return tx.MustNumElementsAtPath(Path(ledger.TxEndorsements))
}

func (tx *Transaction) MustOutputDataAt(idx byte) []byte {
	return tx.MustBytesAtPath(common.Concat(ledger.TxOutputs, idx))
}

func (tx *Transaction) MustProducedOutputAt(idx byte) *ledger.Output {
	ret, err := ledger.OutputFromBytesWithLib(tx.MustOutputDataAt(idx), tx.Library)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) ProducedOutputAt(idx byte) (*ledger.Output, error) {
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
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
	ret, err = base.OutputIDFromBytes(tx.MustBytesAtPath(common.Concat(ledger.TxInputIDs, idx)))
	return
}

func (tx *Transaction) MustInputAt(idx byte) base.OutputID {
	ret, err := tx.InputAt(idx)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) MustOutputIndexOfTheInput(inputIdx byte) byte {
	return base.MustOutputIndexFromIDBytes(tx.MustBytesAtPath(common.Concat(ledger.TxInputIDs, inputIdx)))
}

func (tx *Transaction) InputAtString(idx byte) string {
	ret, err := tx.InputAt(idx)
	if err != nil {
		return err.Error()
	}
	return ret.String()
}

func (tx *Transaction) InputAtShort(idx byte) string {
	ret, err := tx.InputAt(idx)
	if err != nil {
		return err.Error()
	}
	return ret.StringShort()
}

func (tx *Transaction) Inputs() []base.OutputID {
	ret := make([]base.OutputID, tx.NumInputs())
	for i := range ret {
		ret[i] = tx.MustInputAt(byte(i))
	}
	return ret
}

func (tx *Transaction) MustUnlockDataAt(idx byte) []byte {
	return tx.MustBytesAtPath(common.Concat(ledger.TxUnlockData, idx))
}

func (tx *Transaction) ConsumedOutputAt(idx byte, fetchOutput func(id *base.OutputID) ([]byte, bool)) (*ledger.OutputDataWithID, error) {
	oid, err := tx.InputAt(idx)
	if err != nil {
		return nil, err
	}
	ret, ok := fetchOutput(&oid)
	if !ok {
		return nil, fmt.Errorf("can't fetch output %s", oid.StringShort())
	}
	return &ledger.OutputDataWithID{
		ID:   oid,
		Data: ret,
	}, nil
}

func (tx *Transaction) MustEndorsementAt(idx byte) base.TransactionID {
	data := tx.MustBytesAtPath(common.Concat(ledger.TxEndorsements, idx))
	ret, err := base.TransactionIDFromBytes(data)
	util.AssertNoError(err)
	return ret
}

func (tx *Transaction) UnlockParameters(inputIdx, constraintIdx byte) ([]byte, error) {
	ret, err := tx.BytesAtPath(common.Concat(ledger.TxUnlockData, inputIdx, constraintIdx))
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// HashInputsAndEndorsements blake2b of concatenated input IDs and endorsements
// independent of any other tx data but inputs
func (tx *Transaction) HashInputsAndEndorsements() [32]byte {
	var buf bytes.Buffer

	buf.Write(tx.MustBytesAtPath(Path(ledger.TxInputIDs)))
	buf.Write(tx.MustBytesAtPath(Path(ledger.TxEndorsements)))

	return blake2b.Sum256(buf.Bytes())
}

func (tx *Transaction) ForEachInput(fun func(i byte, oid base.OutputID) bool) {
	err := tx.ForEach(func(i byte, data []byte) bool {
		oid, err := base.OutputIDFromBytes(data)
		util.Assertf(err == nil, "ForEachInput @ %d: %v", i, err)
		return fun(i, oid)
	}, Path(ledger.TxInputIDs))
	util.AssertNoError(err)
}

func (tx *Transaction) ForEachEndorsement(fun func(idx byte, txid base.TransactionID) bool) {
	err := tx.ForEach(func(i byte, data []byte) bool {
		txid, err := base.TransactionIDFromBytes(data)
		util.Assertf(err == nil, "ForEachEndorsement @ %d: %v", i, err)
		return fun(i, txid)
	}, Path(ledger.TxEndorsements))
	util.AssertNoError(err)
}

func (tx *Transaction) ForEachOutputData(fun func(idx byte, oData []byte) bool) {
	_ = tx.ForEach(func(i byte, data []byte) bool {
		return fun(i, data)
	}, Path(ledger.TxOutputs))
}

// ForEachProducedOutput traverses all produced outputs
// Inside callback function the correct outputID must be obtained with OutputID(idx byte) ledger.OutputID
// because stem output ID has a special form
func (tx *Transaction) ForEachProducedOutput(fun func(idx byte, o *ledger.Output, oid base.OutputID) bool) {
	tx.ForEachOutputData(func(idx byte, oData []byte) bool {
		o, _ := ledger.OutputFromBytesWithLib(oData, tx.Library)
		oid := tx.OutputID(idx)
		if !fun(idx, o, oid) {
			return false
		}
		return true
	})
}

func (tx *Transaction) PredecessorTransactionIDs() set.Set[base.TransactionID] {
	ret := set.New[base.TransactionID]()
	tx.ForEachInput(func(_ byte, oid base.OutputID) bool {
		ret.Insert(oid.TransactionID())
		return true
	})
	tx.ForEachEndorsement(func(_ byte, txid base.TransactionID) bool {
		ret.Insert(txid)
		return true
	})
	return ret
}

func (tx *Transaction) OutputID(idx byte) base.OutputID {
	return base.MustNewOutputID(tx.ID(), idx)
}

func (tx *Transaction) InflationAmount() uint64 {
	return uint64(tx.producedAmountTotals[ledger.AmountIndexInflation])
}

func OutputWithIDFromTransactionBytes(txBytes []byte, idx byte) (*ledger.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}
	if int(idx) >= tx.NumProducedOutputs() {
		return nil, fmt.Errorf("wrong output index")
	}
	return tx.ProducedOutputWithIDAt(idx)
}

func OutputsWithIDFromTransactionBytes(txBytes []byte) ([]*ledger.OutputWithID, error) {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return nil, err
	}

	ret := make([]*ledger.OutputWithID, tx.NumProducedOutputs())
	for idx := 0; idx < tx.NumProducedOutputs(); idx++ {
		ret[idx], err = tx.ProducedOutputWithIDAt(byte(idx))
		if err != nil {
			return nil, err
		}
	}
	return ret, nil
}

func (tx *Transaction) ToString(fetchOutput func(oid base.OutputID) ([]byte, bool)) string {
	ctx, err := TxContextFromTransaction(tx, func(i byte) (*ledger.Output, error) {
		oid, err1 := tx.InputAt(i)
		if err1 != nil {
			return nil, err1
		}
		oData, ok := fetchOutput(oid)
		if !ok {
			return nil, fmt.Errorf("output %s has not been found", oid.StringShort())
		}
		o, err1 := ledger.OutputFromBytesWithLib(oData, tx.Library)
		if err1 != nil {
			return nil, err1
		}
		return o, nil
	})
	if err != nil {
		return err.Error()
	}
	return ctx.String()
}

func (tx *Transaction) ToStringWithInputLoaderByIndex(fetchOutput func(i byte) (*ledger.Output, error)) string {
	ctx, err := TxContextFromTransaction(tx, fetchOutput)
	if err != nil {
		return err.Error()
	}
	return ctx.String()
}

func (tx *Transaction) InputLoaderByIndex(fetchOutput func(oid base.OutputID) ([]byte, bool)) func(byte) (*ledger.Output, error) {
	return func(idx byte) (*ledger.Output, error) {
		inp := tx.MustInputAt(idx)
		odata, ok := fetchOutput(inp)
		if !ok {
			return nil, fmt.Errorf("can't load input #%d: %s", idx, inp.String())
		}
		// Use transaction's cached library for deterministic parsing.
		// IMPORTANT: Upgrade code is responsible for maintaining backward-compatible
		// bytecode parsing to avoid non-determinism when consuming outputs created
		// with older library versions.
		o, err := ledger.OutputFromBytesWithLib(odata, tx.Library)
		if err != nil {
			return nil, fmt.Errorf("can't load input #%d: %s, '%v'", idx, inp.String(), err)
		}
		return o, nil
	}
}

func (tx *Transaction) InputLoaderFromState(rdr multistate.StateReader) func(idx byte) (*ledger.Output, error) {
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
		tx.ForEachInput(func(i byte, oid base.OutputID) bool {
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
		cc, idx := o.ChainConstraint()
		if idx == 0xff {
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

func (tx *Transaction) EndorsementsVeryShort() string {
	ret := make([]string, tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid base.TransactionID) bool {
		ret[idx] = txid.StringVeryShort()
		return true
	})
	return strings.Join(ret, ", ")
}

func (tx *Transaction) ProducedOutputsToString() string {
	ret := make([]string, 0)
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		ret = append(ret, fmt.Sprintf("  %d :", idx), o.ToString("    "))
		return true
	})
	return strings.Join(ret, "\n")
}

func (tx *Transaction) StateMutations() *multistate.Mutations {
	ret := multistate.NewMutations()
	tx.ForEachInput(func(i byte, oid base.OutputID) bool {
		ret.InsertDelOutputMutation(oid)
		return true
	})
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		ret.InsertAddOutputMutation(oid, o)
		return true
	})
	ret.InsertAddTxMutation(tx.ID(), tx.Slot(), byte(tx.NumProducedOutputs()-1))

	// TODO not correct. ChainIDs of discontinued chains must be deleted. We leave it as is because tx.StateMutations is not used
	//  in the UTXO tangle but mostly in tests
	return ret
}

func (tx *Transaction) Lines(inputLoaderByIndex func(i byte) (*ledger.Output, error), prefix ...string) *lines.Lines {
	ctx, err := TxContextFromTransaction(tx, inputLoaderByIndex)
	if err != nil {
		ret := lines.New(prefix...)
		ret.Add("can't create context of transaction %s: '%v'", tx.IDShortString(), err)
		return ret
	}
	return ctx.Lines(prefix...)
}

func (tx *Transaction) ProducedTagAlongOutputs(targetID ...base.ChainID) []ledger.TagAlongOutput {
	ret := make([]ledger.TagAlongOutput, 0)
	tx.ForEachProducedOutput(func(_ byte, o *ledger.Output, oid base.OutputID) bool {
		out := ledger.OutputWithID{ID: oid, Output: o}
		ta := out.AsTagAlong()
		if ta.TagAlongLock == nil {
			return true
		}
		if len(targetID) > 0 && ta.TagAlongLock.TargetSequencerID != targetID[0] {
			return true
		}
		ret = append(ret, ta)
		return true
	})
	return ret
}

func (tx *Transaction) LinesShort(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	ret.Add("id: %s", tx.IDString())
	sig, err := tx.Signature()
	util.AssertNoError(err)
	ret.Add("Spender ID: %s", sig.SpenderIDHex())
	ret.Add("Total: %s", util.Th(tx.TotalAmount()))
	ret.Add("Inflation: %s", util.Th(tx.InflationAmount()))
	if tx.IsSequencerTransaction() {
		ret.Add("Sequencer output index: %d, Stem output index: %d", tx.sequencerTransactionData.SequencerOutputIndex, tx.sequencerTransactionData.StemOutputIndex)
	}
	ret.Add("Endorsements (%d):", tx.NumEndorsements())
	tx.ForEachEndorsement(func(idx byte, txid base.TransactionID) bool {
		ret.Add("    %3d: %s", idx, txid.String())
		return true
	})
	ret.Add("Inputs (%d):", tx.NumInputs())
	tx.ForEachInput(func(i byte, oid base.OutputID) bool {
		ret.Add("    %3d: %s", i, oid.String())
		ret.Add("       Unlock data: %s", UnlockDataToString(tx.MustUnlockDataAt(i)))
		return true
	})
	ret.Add("Outputs (%d):", tx.NumProducedOutputs())
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	tx.ForEachProducedOutput(func(idx byte, o *ledger.Output, oid base.OutputID) bool {
		ret.Add("%s", oid.StringShort())
		ret.Append(o.Lines(pref + "    "))
		return true
	})
	return ret
}

func (tx *Transaction) String() string {
	return tx.LinesShort().String()
}

func LinesFromTransactionBytes(txBytes []byte, inputLoader func(i byte) (*ledger.Output, error), prefix ...string) *lines.Lines {
	tx, err := FromBytes(txBytes)
	if err != nil {
		return lines.New(prefix...).Add("FromBytes returned: %v", err)
	}
	txCtx, err := TxContextFromTransaction(tx, inputLoader)
	if err != nil {
		return lines.New(prefix...).Add("TxContextFromTransaction returned: %v", err)
	}
	return txCtx.Lines(prefix...)
}

// BaselineDirection is the input, endorsement or explicit baseline of the sequencer transaction where to look for a baseline branch
// It is not a baseline yet, (but it can be one).
// It is assumed tx is a sequencer transaction and not the origin of the sequencer chain
func (tx *Transaction) BaselineDirection() (ret base.TransactionID) {
	util.Assertf(tx.IsSequencerTransaction(), "tx.IsSequencerTransaction()")
	var ok bool
	if ret, ok = tx.ExplicitBaseline(); ok {
		return
	}
	predOid, idx := tx.SequencerChainPredecessor()
	util.Assertf(idx != 0xff, "inconsistency: sequencer milestone cannot be a chain origin. %s hex = %s", tx.IDShortString, tx.IDStringHex)

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

func (tx *Transaction) TotalProducedAmounts() [15]int64 {
	return tx.producedAmountTotals
}

func (tx *Transaction) InputCommitment() []byte {
	return tx.MustBytesAtPath(Path(ledger.TxInputCommitment))
}

func (tx *Transaction) AttachmentCost() int {
	return tx.NumInputs() + tx.NumProducedOutputs()
}
