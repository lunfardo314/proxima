package multistate

import (
	"fmt"
	"sync"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	// Updatable is an updatable ledger state, with the particular root
	// Suitable for chained updates
	// Not-thread safe, should be used individual instance for each parallel update.
	// DB (store) is updated atomically with all mutations in one DB transaction
	Updatable struct {
		trie  *immutable.TrieUpdatable
		store StateStore
	}

	// Readable is a read-only ledger state, with the particular root
	// It is thread-safe. The state itself is read-only, but trie cache needs write-lock with every call
	Readable struct {
		mutex *sync.Mutex
		trie  *immutable.TrieReader
	}

	// RootRecord is a persistent data stored in the DB partition with each state root
	// It contains deterministic values for that state
	RootRecord struct {
		Root        common.VCommitment
		SequencerID base.ChainID
		// Note: CoverageDelta, SlotInflation, FrozenCoverage and Supply are deterministic values calculated from the ledger past cone
		// Each node calculates them itself, and they must be equal on each
		// CoverageDelta in includes FrozenCoverage
		CoverageDelta uint64
		// FrozenCoverage is the sum of all frozen delegation outputs. They are not moved, but their coverage is accounted for
		FrozenCoverage uint64
		// Supply: total supply of the ledger. It is a sum of all outputs on the ledger, including the branch tx outputs
		Supply uint64
		// SlotInflation: total inflation delta from previous root. It is a sum of individual transaction inflation values
		// of the previous slot/past cone. It includes the branch tx inflation itself and does not include inflation of the previous branch
		SlotInflation uint64
		// Number of new transactions in the slot of the branch
		NumTransactions uint32
		// TODO probably there's a need for other deterministic values, such as total number of outputs, of transactions, of chains
	}

	BranchData struct {
		RootRecord
		Stem            *ledger.OutputWithID
		SequencerOutput *ledger.OutputWithID
	}
)

// partitions of the state store on the trie
// Ledger state contains records of UTXOs (keys 33 bytes long output IDs ) and all past transaction IDs (32 byte long keys)
// reason why we put index entries (accounts, chain ChainID) into the trie is because index is ledger state-specific
//
// NOTE: transaction IDs (32 byte long) and UTXO IDs (33 byte long) are on the same partition (1-byte prefix) TriePartitionLedgerState,
// i.e. txs and utxos are distinguished by size of their keys. This is significant optimization of the trie, because txid and tx outputs
// have the same 32 byte long prefix

// TODO optimization: maintain and store UTXO bitmap as a terminal of the txid in the trie

const (
	TriePartitionLedgerState = byte(iota)
	TriePartitionAccounts
	TriePartitionChainID
)

func PartitionToString(p byte) string {
	switch p {
	case TriePartitionLedgerState:
		return "UTXO"
	case TriePartitionAccounts:
		return "ACCN"
	case TriePartitionChainID:
		return "CHID"
	default:
		return "????"
	}
}

func LedgerIdentityBytesFromStore(store StateStore) []byte {
	rr := FetchAnyLatestRootRecord(store)
	return LedgerIdentityBytesFromRoot(store, rr.Root)
}

func LedgerIdentityBytesFromRoot(store StateStoreReader, root common.VCommitment) []byte {
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, root, 0)
	util.AssertNoError(err)
	return trie.Get(nil)
}

// NewReadable creates read-only ledger state with the given root
func NewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) (*Readable, error) {
	trie, err := immutable.NewTrieReader(ledger.CommitmentModel, store, root, clearCacheAtSize...)
	if err != nil {
		return nil, err
	}
	return &Readable{
		mutex: &sync.Mutex{},
		trie:  trie,
	}, nil
}

func MustNewReadable(store common.KVReader, root common.VCommitment, clearCacheAtSize ...int) *Readable {
	ret, err := NewReadable(store, root, clearCacheAtSize...)
	util.AssertNoError(err)
	return ret
}

// NewUpdatable creates updatable state with the given root. After updated, the root changes.
// Suitable for chained updates of the ledger state
func NewUpdatable(store StateStore, root common.VCommitment) (*Updatable, error) {
	trie, err := immutable.NewTrieUpdatable(ledger.CommitmentModel, store, root)
	if err != nil {
		return nil, err
	}
	return &Updatable{
		trie:  trie,
		store: store,
	}, nil
}

func MustNewUpdatable(store StateStore, root common.VCommitment) *Updatable {
	ret, err := NewUpdatable(store, root)
	util.AssertNoError(err)
	return ret
}

func (r *Readable) GetUTXO(oid base.OutputID) ([]byte, bool) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r._getUTXO(oid)
}

func (r *Readable) _getUTXO(oid base.OutputID, partition ...*common.ReaderPartition) ([]byte, bool) {
	var part *common.ReaderPartition
	if len(partition) > 0 {
		part = partition[0]
	} else {
		part = common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
		defer part.Dispose()
	}

	ret := part.Get(oid[:])
	if len(ret) == 0 {
		return nil, false
	}

	return ret, true
}

func (r *Readable) HasUTXO(oid base.OutputID) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	return partition.Has(oid[:])
}

func (r *Readable) KnowsCommittedTransaction(txid base.TransactionID) bool {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	partition := common.MakeTraversableReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	return common.HasWithPrefix(partition, txid[:])
}

func (r *Readable) GetUTXOIDsInAccount(addr ledger.AccountID) ([]base.OutputID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(addr) > 255 {
		return nil, fmt.Errorf("accountID length should be <= 255")
	}
	ret := make([]base.OutputID, 0)
	var oid base.OutputID
	var err error

	accountPrefix := common.Concat(TriePartitionAccounts, byte(len(addr)), addr)
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = base.OutputIDFromBytes(k[len(accountPrefix):])
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

func (r *Readable) GetUTXOsInAccount(addr ledger.AccountID) ([]*ledger.OutputDataWithID, error) {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	ret := make([]*ledger.OutputDataWithID, 0)
	err := r.IterateUTXOsInAccount(addr, func(oid base.OutputID, odata []byte) bool {
		ret = append(ret, &ledger.OutputDataWithID{
			ID:   oid,
			Data: odata,
		})
		return true
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (r *Readable) IterateUTXOsInAccount(addr ledger.AccountID, fun func(oid base.OutputID, odata []byte) bool) (err error) {
	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	return r.IterateUTXOIDsInAccount(addr, func(oid base.OutputID) bool {
		if odata, found := r._getUTXO(oid, partition); found {
			return fun(oid, odata)
		}
		return true
	})
}

func (r *Readable) IsKnownAccount(addr ledger.AccountID) (ret bool) {
	err := r.IterateUTXOsInAccount(addr, func(oid base.OutputID, odata []byte) bool {
		ret = true
		return false
	})
	util.AssertNoError(err)
	return
}

func (r *Readable) IterateUTXOIDsInAccount(addr ledger.AccountID, fun func(oid base.OutputID) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if len(addr) > 255 {
		return fmt.Errorf("accountID length should be <= 255")
	}
	accountPrefix := common.Concat(TriePartitionAccounts, byte(len(addr)), addr)

	var oid base.OutputID

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		oid, err = base.OutputIDFromBytes(k[len(accountPrefix):])
		if err != nil {
			return false
		}
		return fun(oid)
	})
	return err
}

func (r *Readable) GetUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r._getUTXOForChainID(id)
}

func (r *Readable) _getUTXOForChainID(id base.ChainID) (*ledger.OutputDataWithID, error) {
	chainPartition := common.MakeReaderPartition(r.trie, TriePartitionChainID)
	outID := chainPartition.Get(id[:])
	defer chainPartition.Dispose()

	if len(outID) == 0 {
		return nil, ErrNotFound
	}
	oid, err := base.OutputIDFromBytes(outID)
	if err != nil {
		return nil, err
	}
	outData, found := r._getUTXO(oid)

	if !found {
		return nil, ErrNotFound
	}
	return &ledger.OutputDataWithID{
		ID:   oid,
		Data: outData,
	}, nil
}

func (r *Readable) GetStem() (uint32, []byte) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	accountPrefix := common.Concat(TriePartitionAccounts, byte(len(ledger.StemAccountID)), ledger.StemAccountID)

	var found bool
	var retSlot uint32
	var retBytes []byte

	partition := common.MakeReaderPartition(r.trie, TriePartitionLedgerState)
	defer partition.Dispose()

	// we iterate one element. Stem output ust always be present in the state
	count := 0
	r.trie.Iterator(accountPrefix).IterateKeys(func(k []byte) bool {
		util.Assertf(count == 0, "inconsistency: must be exactly 1 index record for stem output")
		count++
		oid, err := base.OutputIDFromBytes(k[len(accountPrefix):])
		util.AssertNoError(err)
		retSlot = oid.Slot()
		retBytes, found = r._getUTXO(oid, partition)
		util.Assertf(found, "can't find stem output")
		return true
	})
	return retSlot, retBytes
}

func (r *Readable) MustLedgerIdentityBytes() []byte {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.trie.Get(nil)
}

func (r *Readable) Iterator(prefix []byte) common.KVIterator {
	return r.trie.Iterator(prefix)
}

// IterateKnownCommittedTransactions iterates transaction IDs in the state. Optionally, iteration is restricted
// for a slot. In that case first iterates non-sequencer transactions, the sequencer transactions
func (r *Readable) IterateKnownCommittedTransactions(fun func(txid base.TransactionID, slot uint32) bool, txidSlot ...uint32) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	keyPrefix := []byte{TriePartitionLedgerState}
	if len(txidSlot) > 0 {
		keyPrefix = append(keyPrefix, base.Slot2Bytes(txidSlot[0])...)
	}
	r.trie.Iterator(keyPrefix).Iterate(func(k, v []byte) bool {
		d := k[1:]
		if len(d) != base.TransactionIDLength {
			return true
		}
		txid := base.MustTransactionIDFromBytes(d)
		slot, err := base.SlotFromBytes(v)
		util.AssertNoError(err)

		return fun(txid, slot)
	})
}

func (r *Readable) KnownCommittedTxIDs(slot uint32) []base.TransactionID {
	ret := make([]base.TransactionID, 0)
	r.IterateKnownCommittedTransactions(func(txid base.TransactionID, _ uint32) bool {
		ret = append(ret, txid)
		return true
	}, slot)
	return ret
}

func (r *Readable) IterateChainTips(fun func(chainID base.ChainID, oid base.OutputID) bool) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var chainID base.ChainID
	var oid base.OutputID
	var err error
	r.trie.Iterator([]byte{TriePartitionChainID}).Iterate(func(k []byte, v []byte) bool {
		chainID, err = base.ChainIDFromBytes(k[1:])
		if err != nil {
			return false
		}
		oid, err = base.OutputIDFromBytes(v)
		if err != nil {
			return false
		}
		return fun(chainID, oid)
	})
	return err
}

func (r *Readable) Root() common.VCommitment {
	// non need to lock
	return r.trie.Root()
}

func (r *Readable) IterateUTXOs(fun func(o ledger.OutputWithID) bool) (err error) {
	r.mutex.Lock()
	fmt.Printf(">>>>>>>>>>>>>>>> after lock\n")
	defer r.mutex.Unlock()

	var oid base.OutputID
	var o *ledger.Output

	i := 0
	r.trie.Iterator([]byte{TriePartitionLedgerState}).Iterate(func(key, oData []byte) bool {
		fmt.Printf(">>>>>>>>>>>>>>>> iterate %d\n", i)
		i++

		d := key[1:]
		if len(d) != base.OutputIDLength {
			return true
		}
		if oid, err = base.OutputIDFromBytes(d); err != nil {
			return false
		}
		if o, err = ledger.OutputFromBytes(oData); err != nil {
			return false
		}
		return fun(ledger.OutputWithID{
			ID:     oid,
			Output: o,
		})
	})
	return
}

func (r *Readable) IterateUTXOsInSlot(slot uint32, fun func(oid base.OutputID, oData []byte) bool) (err error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	prefix := common.Concat(TriePartitionLedgerState, base.Slot2Bytes(slot))

	var oid base.OutputID
	r.trie.Iterator(prefix).Iterate(func(k, v []byte) bool {
		d := k[1:]
		if len(d) != base.OutputIDLength {
			return true
		}
		if oid, err = base.OutputIDFromBytes(d); err != nil {
			return false
		}
		return fun(oid, v)
	})
	return err
}

func (u *Updatable) Readable() *Readable {
	return &Readable{
		mutex: &sync.Mutex{},
		trie:  u.trie.TrieReader,
	}
}

func (u *Updatable) Root() common.VCommitment {
	return u.trie.Root()
}

type RootRecordParams struct {
	StemOutputID      base.OutputID
	SeqID             base.ChainID
	CoverageDelta     uint64
	FrozenCoverage    uint64
	SlotInflation     uint64
	Supply            uint64
	NumTransactions   uint32
	WriteEarliestSlot bool
}

// Update updates trie with mutations
// If par.GenesisStemOutputID != nil, also writes root partition record
func (u *Updatable) Update(muts *Mutations, rootRecordParams *RootRecordParams) error {
	var slotInflation []uint64
	if rootRecordParams != nil {
		slotInflation = []uint64{rootRecordParams.SlotInflation}
	}

	err := u.updateUTXOLedgerDB(func(trie *immutable.TrieUpdatable) error {
		return updateTrie(u.trie, muts, slotInflation...)
	}, rootRecordParams)
	if err != nil {
		err = fmt.Errorf("%w\n-------- mutations --------\n%s", err, muts.Lines("    ").String())
	}
	return err
}

func (u *Updatable) MustUpdate(muts *Mutations, par *RootRecordParams) {
	err := u.Update(muts, par)
	util.AssertNoError(err)
}

func (u *Updatable) updateUTXOLedgerDB(updateFun func(updatable *immutable.TrieUpdatable) error, rootRecordsParams *RootRecordParams) error {
	if err := updateFun(u.trie); err != nil {
		return err
	}
	batch := u.store.BatchedWriter()
	newRoot := u.trie.Commit(batch)
	if rootRecordsParams != nil {
		latestSlot := FetchLatestCommittedSlot(u.store)
		if latestSlot < rootRecordsParams.StemOutputID.Slot() {
			WriteLatestSlotRecord(batch, rootRecordsParams.StemOutputID.Slot())
		}
		if rootRecordsParams.WriteEarliestSlot {
			WriteEarliestSlotRecord(batch, rootRecordsParams.StemOutputID.Slot())
		}
		branchID := rootRecordsParams.StemOutputID.TransactionID()
		WriteRootRecord(batch, branchID, RootRecord{
			Root:            newRoot,
			SequencerID:     rootRecordsParams.SeqID,
			CoverageDelta:   rootRecordsParams.CoverageDelta,
			FrozenCoverage:  rootRecordsParams.FrozenCoverage,
			SlotInflation:   rootRecordsParams.SlotInflation,
			Supply:          rootRecordsParams.Supply,
			NumTransactions: rootRecordsParams.NumTransactions,
		})
	}
	var err error
	if err = batch.Commit(); err != nil {
		return err
	}
	if u.trie, err = immutable.NewTrieUpdatable(ledger.CommitmentModel, u.store, newRoot); err != nil {
		return err
	}
	return nil
}
