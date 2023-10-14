package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	// Updatable is an updatable ledger state, with the particular root
	// Suitable for chained updates
	Updatable struct {
		trie  *immutable.TrieUpdatable
		store general.StateStore
	}

	// Readable is a read-only ledger state, with the particular root
	Readable struct {
		trie *immutable.TrieReader
	}

	RootRecord struct {
		Root        common.VCommitment
		SequencerID core.ChainID
		Coverage    uint64
	}

	BranchData struct {
		RootRecord
		Stem      *core.OutputWithID
		SeqOutput *core.OutputWithID
	}
)

// partitions of the state store on the trie

const (
	PartitionLedgerState = byte(iota)
	PartitionAccounts
	PartitionChainID
	PartitionTransactionID
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
func NewUpdatable(store general.StateStore, root common.VCommitment) (*Updatable, error) {
	trie, err := immutable.NewTrieUpdatable(core.CommitmentModel, store, root)
	if err != nil {
		return nil, err
	}
	return &Updatable{
		trie:  trie,
		store: store,
	}, nil
}

func MustNewUpdatable(store general.StateStore, root common.VCommitment) *Updatable {
	ret, err := NewUpdatable(store, root)
	util.AssertNoError(err)
	return ret
}

func (u *Updatable) Desugar() general.IndexedStateReader {
	return u.Readable()
}

func (u *Updatable) Readable() *Readable {
	return &Readable{u.trie.TrieReader}
}

func (r *Readable) GetUTXO(oid *core.OutputID) ([]byte, bool) {
	ret := common.MakeReaderPartition(r.trie, PartitionLedgerState).Get(oid[:])
	if len(ret) == 0 {
		return nil, false
	}
	return ret, true
}

func (r *Readable) Desugar() general.IndexedStateReader {
	return r
}

func (r *Readable) HasUTXO(oid *core.OutputID) bool {
	return common.MakeReaderPartition(r.trie, PartitionLedgerState).Has(oid[:])
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
	r.trie.Iterator(accountPrefix).Iterate(func(k, _ []byte) bool {
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
		return nil, fmt.Errorf("GetUTXOForChainID: chain id: %s, outputID: %s. Output has not been found", id.Short(), oid.Short())
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

func (r *Readable) HasTransaction(txid *core.TransactionID) bool {
	return common.HasWithPrefix(common.MakeTraversableReaderPartition(r.trie, PartitionLedgerState), txid[:])
}

func (r *Readable) Root() common.VCommitment {
	return r.trie.Root()
}

func (u *Updatable) Root() common.VCommitment {
	return u.trie.Root()
}

// UpdateWithCommands updates trie with commands.
// If rootStemOutputID != nil, also writes root partition record
func (u *Updatable) UpdateWithCommands(cmds []UpdateCmd, rootStemOutputID *core.OutputID, rootSeqID *core.ChainID, coverage uint64) error {
	return u.updateUTXOLedgerDB(func(trie *immutable.TrieUpdatable) error {
		return UpdateTrieOld(u.trie, cmds)
	}, rootStemOutputID, rootSeqID, coverage)
}

func (u *Updatable) MustUpdateWithCommands(cmds []UpdateCmd, rootStemOutputID *core.OutputID, rootSeqID *core.ChainID, coverage uint64) {
	err := u.UpdateWithCommands(cmds, rootStemOutputID, rootSeqID, coverage)
	common.AssertNoError(err)
}

func (u *Updatable) updateUTXOLedgerDB(updateFun func(updatable *immutable.TrieUpdatable) error, stemOutputID *core.OutputID, seqID *core.ChainID, coverage uint64) error {
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
			Root:        newRoot,
			SequencerID: *seqID,
			Coverage:    coverage,
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
