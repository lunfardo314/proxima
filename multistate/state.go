package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	// Updatable is an updatable ledger state, with the particular root
	// Suitable for chained updates
	Updatable struct {
		trie  *immutable.TrieUpdatable
		store global.StateStore
	}

	// Readable is a read-only ledger state, with the particular root
	Readable struct {
		trie *immutable.TrieReader
	}

	LedgerCoverage [HistoryCoverageDeltas]uint64

	RootRecord struct {
		Root           common.VCommitment
		SequencerID    core.ChainID
		LedgerCoverage LedgerCoverage
	}

	BranchData struct {
		RootRecord
		Stem            *core.OutputWithID
		SequencerOutput *core.OutputWithID
	}
)

// partitions of the state store on the trie

const HistoryCoverageDeltas = 2

func init() {
	util.Assertf(HistoryCoverageDeltas*8 <= 256, "HistoryCoverageDeltas*8 <= 256")
}

const (
	PartitionLedgerState = byte(iota)
	PartitionAccounts
	PartitionChainID
	PartitionCommittedTransactionID
)

// NewReadable creates read-only ledger state with the given root
func NewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) (*Readable, error) {
	trie, err := immutable.NewTrieReader(core.CommitmentModel, store, root, clearCacheAtSize...)
	if err != nil {
		return nil, err
	}
	return &Readable{trie}, nil
}

func MustNewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) *Readable {
	ret, err := NewReadable(store, root, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

func MustNewSugaredStateReader(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) SugaredStateReader {
	return MakeSugared(MustNewReadable(store, root, clearCacheAtSize...))
}

// NewUpdatable creates updatable state with the given root. After updated, the root changes.
// Suitable for chained updates of the ledger state
func NewUpdatable(store global.StateStore, root common.VCommitment) (*Updatable, error) {
	trie, err := immutable.NewTrieUpdatable(core.CommitmentModel, store, root)
	if err != nil {
		return nil, err
	}
	return &Updatable{
		trie:  trie,
		store: store,
	}, nil
}

func MustNewUpdatable(store global.StateStore, root common.VCommitment) *Updatable {
	ret, err := NewUpdatable(store, root)
	util.AssertNoError(err)
	return ret
}

func (r *Readable) GetUTXO(oid *core.OutputID) ([]byte, bool) {
	ret := common.MakeReaderPartition(r.trie, PartitionLedgerState).Get(oid[:])
	if len(ret) == 0 {
		return nil, false
	}
	return ret, true
}

func (r *Readable) HasUTXO(oid *core.OutputID) bool {
	return common.MakeReaderPartition(r.trie, PartitionLedgerState).Has(oid[:])
}

// KnowsCommittedTransaction transaction IDs are purged after some time, so the result may be
func (r *Readable) KnowsCommittedTransaction(txid *core.TransactionID) bool {
	return common.MakeReaderPartition(r.trie, PartitionCommittedTransactionID).Has(txid[:])
}

func (r *Readable) GetIDSLockedInAccount(addr core.AccountID) ([]core.OutputID, error) {
	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	ret := make([]core.OutputID, 0)
	var oid core.OutputID
	var err error

	accountPrefix := common.Concat(PartitionAccounts, byte(len(addr)), addr)
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = core.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		ret = append(ret, oid)
		return true
	})

	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *Readable) GetUTXOsLockedInAccount(addr core.AccountID) ([]*core.OutputDataWithID, error) {
	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	accountPrefix := common.Concat(PartitionAccounts, byte(len(addr)), addr)

	ret := make([]*core.OutputDataWithID, 0)
	var err error
	var found bool
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		o := &core.OutputDataWithID{}
		o.ID, err = core.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		o.OutputData, found = r.GetUTXO(&o.ID)
		if !found {
			// skip this output ID
			return true
		}
		ret = append(ret, o)
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, err
}

func (r *Readable) GetUTXOForChainID(id *core.ChainID) (*core.OutputDataWithID, error) {
	if len(id) != core.ChainIDLength {
		return nil, fmt.Errorf("GetUTXOForChainID: chainID length must be %d-bytes long", core.ChainIDLength)
	}
	outID := common.MakeReaderPartition(r.trie, PartitionChainID).Get(id[:])
	if len(outID) == 0 {
		return nil, ErrNotFound
	}
	oid, err := core.OutputIDFromBytes(outID)
	if err != nil {
		return nil, err
	}
	outData, found := r.GetUTXO(&oid)
	if !found {
		return nil, ErrNotFound
	}
	return &core.OutputDataWithID{
		ID:         oid,
		OutputData: outData,
	}, nil
}

func (r *Readable) GetStem() (core.TimeSlot, []byte) {
	accountPrefix := common.Concat(PartitionAccounts, byte(len(core.StemAccountID)), core.StemAccountID)

	var found bool
	var retSlot core.TimeSlot
	var retBytes []byte

	// we iterate one element. Stem output ust always be present in the state
	count := 0
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		util.Assertf(count == 0, "inconsistency: must be exactly 1 index record for stem output")
		count++
		id, err := core.OutputIDFromBytes(k[len(accountPrefix):])
		util.AssertNoError(err)
		retSlot = id.TimeSlot()
		retBytes, found = r.GetUTXO(&id)
		util.Assertf(found, "can't find stem output")
		return true
	})
	return retSlot, retBytes
}

func (r *Readable) MustStateIdentityBytes() []byte {
	return r.trie.Get(nil)
}

func (r *Readable) HasTransactionOutputs(txid *core.TransactionID, indexMap ...map[byte]struct{}) (bool, bool) {
	hasTransaction := false
	allOutputsExist := false
	count := 0

	iter := common.MakeTraversableReaderPartition(r.trie, PartitionLedgerState).Iterator(txid[:])
	iter.IterateKeys(func(k []byte) bool {
		hasTransaction = true
		if len(indexMap) == 0 {
			allOutputsExist = true
			return false
		}
		if _, ok := indexMap[0][core.MustOutputIDIndexFromBytes(k)]; ok {
			count++
		}
		if count == len(indexMap) {
			allOutputsExist = true
			return false
		}
		return true
	})
	return hasTransaction, allOutputsExist
}

// IterateKnownCommittedTransactions utility function to collect old transaction IDs which may be purged from the state
// Those txid serve no purpose after corresponding branches become committed and may appear only as virtual transactions
func (r *Readable) IterateKnownCommittedTransactions(fun func(txid *core.TransactionID, slot core.TimeSlot) bool) {
	iter := common.MakeTraversableReaderPartition(r.trie, PartitionCommittedTransactionID).Iterator(nil)
	var slot core.TimeSlot
	iter.Iterate(func(k, v []byte) bool {
		txid, err := core.TransactionIDFromBytes(k)
		util.AssertNoError(err)
		slot, err = core.TimeSlotFromBytes(v)
		util.AssertNoError(err)

		return fun(&txid, slot)
	})
}

func (r *Readable) AccountsByLocks() map[string]LockedAccountInfo {
	var oid core.OutputID
	var err error

	ret := make(map[string]LockedAccountInfo)

	r.trie.Iterator([]byte{PartitionAccounts}).IterateKeys(func(k []byte) bool {
		oid, err = core.OutputIDFromBytes(k[2+k[1]:])
		util.AssertNoError(err)

		oData, found := r.GetUTXO(&oid)
		util.Assertf(found, "can't get output")

		_, amount, lock, err := core.OutputFromBytesMain(oData)
		util.AssertNoError(err)

		lockStr := lock.String()
		lockInfo := ret[lockStr]
		lockInfo.Balance += uint64(amount)
		lockInfo.NumOutputs++
		ret[lockStr] = lockInfo

		return true
	})
	return ret
}

func (r *Readable) ChainInfo() map[core.ChainID]ChainRecordInfo {
	ret := make(map[core.ChainID]ChainRecordInfo)
	var chainID core.ChainID
	var err error
	var oData *core.OutputDataWithID
	var amount core.Amount

	r.trie.Iterator([]byte{PartitionChainID}).Iterate(func(k, v []byte) bool {
		chainID, err = core.ChainIDFromBytes(k[1:])
		util.AssertNoError(err)
		oData, err = r.GetUTXOForChainID(&chainID)
		util.AssertNoError(err)

		_, already := ret[chainID]
		util.Assertf(!already, "repeating chain record")
		_, amount, _, err = core.OutputFromBytesMain(oData.OutputData)
		util.AssertNoError(err)

		ret[chainID] = ChainRecordInfo{
			Balance:     uint64(amount),
			IsSequencer: oData.ID.IsSequencerTransaction(),
			IsBranch:    oData.ID.IsBranchTransaction(),
		}
		return true
	})
	return ret
}

func (r *Readable) Root() common.VCommitment {
	return r.trie.Root()
}

func (u *Updatable) Readable() *Readable {
	return &Readable{u.trie.TrieReader}
}

func (u *Updatable) Root() common.VCommitment {
	return u.trie.Root()
}

// Update updates trie with mutations
// If rootStemOutputID != nil, also writes root partition record
func (u *Updatable) Update(muts *Mutations, rootStemOutputID *core.OutputID, rootSeqID *core.ChainID, coverage LedgerCoverage) error {
	return u.updateUTXOLedgerDB(func(trie *immutable.TrieUpdatable) error {
		return UpdateTrie(u.trie, muts)
	}, rootStemOutputID, rootSeqID, coverage)
}

func (u *Updatable) MustUpdate(muts *Mutations, rootStemOutputID *core.OutputID, rootSeqID *core.ChainID, coverage LedgerCoverage) {
	err := u.Update(muts, rootStemOutputID, rootSeqID, coverage)
	common.AssertNoError(err)
}

func (u *Updatable) updateUTXOLedgerDB(updateFun func(updatable *immutable.TrieUpdatable) error, stemOutputID *core.OutputID, seqID *core.ChainID, coverage LedgerCoverage) error {
	if err := updateFun(u.trie); err != nil {
		return err
	}
	batch := u.store.BatchedWriter()
	newRoot := u.trie.Commit(batch)
	if stemOutputID != nil {
		latestSlot := FetchLatestSlot(u.store)
		if latestSlot < stemOutputID.TimeSlot() {
			writeLatestSlot(batch, stemOutputID.TimeSlot())
		}
		writeRootRecord(batch, stemOutputID.TransactionID(), RootRecord{
			Root:           newRoot,
			SequencerID:    *seqID,
			LedgerCoverage: coverage,
		})
	}
	var err error
	if err = batch.Commit(); err != nil {
		return err
	}
	if u.trie, err = immutable.NewTrieUpdatable(core.CommitmentModel, u.store, newRoot); err != nil {
		return err
	}
	return nil
}

// BranchIsDescendantOf returns true if predecessor txid is known in the descendents state
func BranchIsDescendantOf(descendant, predecessor *core.TransactionID, getStore func() global.StateStore) bool {
	util.Assertf(descendant.BranchFlagON(), "must be a branch ts")

	if core.EqualTransactionIDs(descendant, predecessor) {
		return true
	}
	if descendant.Timestamp().Before(predecessor.Timestamp()) {
		return false
	}
	store := getStore()
	rr, found := FetchRootRecord(store, *descendant)
	if !found {
		return false
	}
	rdr, err := NewReadable(store, rr.Root)
	if err != nil {
		return false
	}

	return rdr.KnowsCommittedTransaction(predecessor)
}
