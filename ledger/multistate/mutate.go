package multistate

import (
	"fmt"
	"slices"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/proxima/util/set256"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

// txBitmapCache tracks in-memory modifications to TX record bitmaps during a batch
// of trie mutations. This is necessary because TrieUpdatable.Get() reads from the
// persistent (committed) state, not from the buffered (mutated) state. Without this
// cache, when multiple DEL mutations target outputs of the same TX, each would read
// the ORIGINAL bitmap and the last write would overwrite all previous changes.
type txBitmapCache map[base.TransactionID]*set256.Set256

type (
	mutationCmd interface {
		mutate(trie *immutable.TrieUpdatable, gcSlot uint32, bitmapCache txBitmapCache) (delta supplyDelta, err error)
		text() string
		sortOrder() byte
		timestamp() base.LedgerTime
	}

	supplyDelta struct {
		amount   uint64
		decrease bool
	}

	mutationAddOutput struct {
		ID     base.OutputID
		Output *ledger.Output
	}

	mutationDelOutput struct {
		ID base.OutputID
	}

	mutationAddTx struct {
		ID             base.TransactionID
		UnspentOutputs set256.Set256
	}

	mutationDelTx struct {
		ID base.TransactionID
	}

	mutationDelChain struct {
		ChainID base.ChainID
	}

	Mutations struct {
		mut    []mutationCmd
		GCSlot uint32 // slot threshold: TX records at or before this slot are pruned when their unspent set becomes empty
	}
)

func (m *mutationDelOutput) mutate(trie *immutable.TrieUpdatable, gcSlot uint32, bitmapCache txBitmapCache) (delta supplyDelta, err error) {
	return deleteOutputFromTrie(trie, m.ID, gcSlot, bitmapCache)
}

func (m *mutationDelOutput) text() string {
	return fmt.Sprintf("DEL   %s", m.ID.StringShort())
}

func (m *mutationDelOutput) sortOrder() byte {
	return 0
}

func (m *mutationDelOutput) timestamp() base.LedgerTime {
	return m.ID.Timestamp()
}

func (m *mutationAddOutput) mutate(trie *immutable.TrieUpdatable, _ uint32, _ txBitmapCache) (delta supplyDelta, err error) {
	return addOutputToTrie(trie, m.ID, m.Output)
}

func (m *mutationAddOutput) text() string {
	return fmt.Sprintf("ADD   %s (%s, inflation %s)", m.ID.StringShort(), util.Th(m.Output.TokenBalance()), util.Th(m.Output.Inflation()))
}

func (m *mutationAddOutput) sortOrder() byte {
	return 1
}

func (m *mutationAddOutput) timestamp() base.LedgerTime {
	return m.ID.Timestamp()
}

func (m *mutationAddTx) mutate(trie *immutable.TrieUpdatable, _ uint32, bitmapCache txBitmapCache) (delta supplyDelta, err error) {
	// Register the bitmap in the cache so subsequent DEL mutations for this TX
	// (if any) see the correct starting bitmap rather than stale persistent data
	s := m.UnspentOutputs // copy
	bitmapCache[m.ID] = &s
	return addTxToTrie(trie, &m.ID, &m.UnspentOutputs)
}

func (m *mutationAddTx) text() string {
	return fmt.Sprintf("ADDTX %s : unspent %v", m.ID.StringShort(), m.UnspentOutputs.Elements())
}

func (m *mutationAddTx) sortOrder() byte {
	return 2
}

func (m *mutationAddTx) timestamp() base.LedgerTime {
	return m.ID.Timestamp()
}

func (m *mutationDelTx) mutate(trie *immutable.TrieUpdatable, _ uint32, bitmapCache txBitmapCache) (delta supplyDelta, err error) {
	delete(bitmapCache, m.ID)
	err = delTxFromTrie(trie, &m.ID)
	return
}

func (m *mutationDelTx) text() string {
	return fmt.Sprintf("DELTX %s", m.ID.StringShort())
}

func (m *mutationDelTx) sortOrder() byte {
	return 3
}

func (m *mutationDelTx) timestamp() base.LedgerTime {
	return m.ID.Timestamp()
}

func (m *mutationDelChain) mutate(trie *immutable.TrieUpdatable, _ uint32, _ txBitmapCache) (delta supplyDelta, err error) {
	return deleteChainFromTrie(trie, m.ChainID)
}

func (m *mutationDelChain) text() string {
	return fmt.Sprintf("DELCH %s", m.ChainID.StringShort())
}

func (m *mutationDelChain) sortOrder() byte {
	return 3
}

func (m *mutationDelChain) timestamp() base.LedgerTime {
	return base.T(0xffffffff, 0xff)
}

func NewMutations() *Mutations {
	return &Mutations{
		mut: make([]mutationCmd, 0),
	}
}

func (mut *Mutations) Len() int {
	return len(mut.mut)
}

func (mut *Mutations) Sort() *Mutations {
	sort.Slice(mut.mut, func(i, j int) bool {
		return mut.mut[i].sortOrder() < mut.mut[j].sortOrder()
	})
	return mut
}

func (mut *Mutations) InsertAddOutputMutation(id base.OutputID, o *ledger.Output) {
	mut.mut = append(mut.mut, &mutationAddOutput{
		ID:     id,
		Output: o.Clone(),
	})
}

// InsertAddOutputMutationRaw inserts an output without validating its lock.
// Use this for special outputs like upgrade UTXOs that don't have standard locks.
func (mut *Mutations) InsertAddOutputMutationRaw(id base.OutputID, o *ledger.Output) {
	mut.mut = append(mut.mut, &mutationAddOutput{
		ID:     id,
		Output: o.CloneRaw(),
	})
}

func (mut *Mutations) InsertDelOutputMutation(id base.OutputID) {
	mut.mut = append(mut.mut, &mutationDelOutput{ID: id})
}

func (mut *Mutations) InsertAddTxMutation(id base.TransactionID, unspentOutputs set256.Set256) {
	mut.mut = append(mut.mut, &mutationAddTx{
		ID:             id,
		UnspentOutputs: unspentOutputs,
	})
}

func (mut *Mutations) InsertDelChainMutation(id base.ChainID) {
	mut.mut = append(mut.mut, &mutationDelChain{id})
}

func (mut *Mutations) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	mutClone := slices.Clone(mut.mut)
	sort.Slice(mutClone, func(i, j int) bool {
		if mutClone[i].sortOrder() < mutClone[j].sortOrder() {
			return true
		}
		if mutClone[i].sortOrder() == mutClone[j].sortOrder() {
			return mutClone[i].timestamp().Before(mutClone[j].timestamp())
		}
		return false
	})
	for _, m := range mutClone {
		ret.Add(m.text())
	}
	return ret
}

// FindAddedOutput looks for an output that was added in these mutations by its ID.
// Returns the output and true if found, nil and false otherwise.
func (mut *Mutations) FindAddedOutput(oid base.OutputID) (*ledger.Output, bool) {
	for _, m := range mut.mut {
		addOut, ok := m.(*mutationAddOutput)
		if !ok {
			continue
		}
		if addOut.ID == oid {
			return addOut.Output, true
		}
	}
	return nil, false
}

// HasDeletedOutput checks if an output was deleted (consumed) in these mutations.
func (mut *Mutations) HasDeletedOutput(oid base.OutputID) bool {
	for _, m := range mut.mut {
		delOut, ok := m.(*mutationDelOutput)
		if !ok {
			continue
		}
		if delOut.ID == oid {
			return true
		}
	}
	return false
}

// FindChainOutput scans mutations for an added output with matching chain constraint.
// Used to look up chain outputs in pending (uncommitted) branches without forcing a DB commit.
func (mut *Mutations) FindChainOutput(chainID base.ChainID) (*ledger.OutputWithID, bool) {
	for _, m := range mut.mut {
		addOut, ok := m.(*mutationAddOutput)
		if !ok {
			continue
		}
		cc := addOut.Output.ChainConstraint()
		if cc == nil {
			continue
		}
		var outputChainID base.ChainID
		if cc.IsOrigin() {
			outputChainID = base.MakeOriginChainID(addOut.ID)
		} else {
			outputChainID = cc.ChainID
		}
		if outputChainID == chainID {
			return &ledger.OutputWithID{ID: addOut.ID, Output: addOut.Output.Clone()}, true
		}
	}
	return nil, false
}

// IsChainDeleted checks if the chain was terminated in these mutations.
func (mut *Mutations) IsChainDeleted(chainID base.ChainID) bool {
	for _, m := range mut.mut {
		if delChain, ok := m.(*mutationDelChain); ok {
			if delChain.ChainID == chainID {
				return true
			}
		}
	}
	return false
}

// HasTx checks if a transaction was added in these mutations.
func (mut *Mutations) HasTx(txid base.TransactionID) bool {
	for _, m := range mut.mut {
		if addTx, ok := m.(*mutationAddTx); ok {
			if addTx.ID == txid {
				return true
			}
		}
	}
	return false
}

// HasDeletedTx checks if a transaction was deleted (expired) in these mutations.
func (mut *Mutations) HasDeletedTx(txid base.TransactionID) bool {
	for _, m := range mut.mut {
		if delTx, ok := m.(*mutationDelTx); ok {
			if delTx.ID == txid {
				return true
			}
		}
	}
	return false
}

func (mut *Mutations) DeleteTxIDs(txid ...base.TransactionID) {
	for i := range txid {
		mut.mut = append(mut.mut, &mutationDelTx{
			ID: txid[i],
		})
	}
}

func deleteOutputFromTrie(trie *immutable.TrieUpdatable, oid base.OutputID, gcSlot uint32, bitmapCache txBitmapCache) (delta supplyDelta, err error) {
	var stateKey [1 + base.OutputIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], oid[:])

	oData := trie.Get(stateKey[:])
	if len(oData) == 0 {
		err = fmt.Errorf("deleteOutputFromTrie: output not found: %s", oid.StringShort())
		return
	}

	// Use output's slot for parsing
	o, err := ledger.OutputFromBytes(oData)
	util.AssertNoError(err)

	delta.decrease = true
	delta.amount = o.TokenBalance()

	var existed bool
	existed = trie.Delete(stateKey[:])
	util.Assertf(existed, "deleteOutputFromTrie: inconsistency while deleting output %s", oid.StringShort())

	for _, accountable := range o.Lock().Controllers() {
		existed = trie.Delete(makeAccountKey(accountable.ControllerID(), oid))
		// must exist
		util.Assertf(existed, "deleteOutputFromTrie: account record for %s wasn't found as expected: output %s", accountable.String(), oid.StringShort())
	}

	// Update the parent txID record: remove this output index from the unspent Set256.
	// If the set becomes empty and the TX is beyond the GC threshold, delete the TX record
	// to avoid leaving orphaned TX records as garbage in the trie
	updateTxUnspentSet(trie, oid.TransactionID(), oid.Index(), false, gcSlot, bitmapCache)
	return
}

// updateTxUnspentSet modifies the unspent outputs Set256 in the txID record.
// If add is true, inserts the index; if false, removes it.
// If the txID record doesn't exist, does nothing (it may have been pruned).
// When removing an index results in an empty set and the TX's slot is at or before gcSlot,
// the TX record is deleted from the trie (late GC for TXs that had unspent outputs when
// their slot was first scanned for pruning).
//
// IMPORTANT: bitmapCache is used to track in-memory bitmap state across multiple mutations
// in the same batch. TrieUpdatable.Get() reads from the persistent (committed) state, not
// from the buffered state. Without this cache, multiple DEL mutations for the same TX would
// each read the ORIGINAL bitmap, and the last write would overwrite all previous changes.
func updateTxUnspentSet(trie *immutable.TrieUpdatable, txid base.TransactionID, index byte, add bool, gcSlot uint32, bitmapCache txBitmapCache) {
	var txKey [1 + base.TransactionIDLength]byte
	txKey[0] = TriePartitionLedgerState
	copy(txKey[1:], txid[:])

	var s set256.Set256
	if cached, ok := bitmapCache[txid]; ok {
		s = *cached
	} else {
		// First access to this TX's bitmap in this batch — read from persistent trie
		txValue := trie.Get(txKey[:])
		if len(txValue) == 0 {
			// txID record not present (possibly pruned), nothing to update
			return
		}
		s = set256.NewFromSlice(txValue)
	}
	if add {
		s.Insert(index)
	} else {
		s.Remove(index)
	}
	// Store the updated bitmap in the cache for subsequent mutations
	bitmapCache[txid] = &s

	if s.IsEmpty() && gcSlot > 0 && txid.Slot() <= gcSlot {
		// All outputs consumed and TX is beyond the GC threshold: delete the TX record
		trie.Delete(txKey[:])
		delete(bitmapCache, txid)
		return
	}
	newValue := s.Bytes()
	if newValue == nil {
		newValue = []byte{0}
	}
	trie.Update(txKey[:], newValue)
}

func addOutputToTrie(trie *immutable.TrieUpdatable, oid base.OutputID, out *ledger.Output) (delta supplyDelta, err error) {
	delta.amount = out.TokenBalance()

	var stateKey [1 + base.OutputIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], oid[:])
	if trie.Update(stateKey[:], out.Bytes()) {
		// key should not exist
		err = fmt.Errorf("addOutputToTrie: UTXO key should not exist: %s", oid.StringShort())
		return
	}
	// Skip account indexing for upgrade UTXOs (they don't have parseable locks)
	if !base.IsUpgradeOutputID(oid) {
		for _, accountable := range out.Lock().Controllers() {
			if trie.Update(makeAccountKey(accountable.ControllerID(), oid), []byte{0xff}) {
				// key should not exist
				err = fmt.Errorf("addOutputToTrie: index key should not exist: %s", oid.StringShort())
				return
			}
		}
	}
	chainConstraint := out.ChainConstraint()
	if chainConstraint == nil {
		// not a chain output
		return
	}
	// update chain output records
	var chainID base.ChainID
	if chainConstraint.IsOrigin() {
		chainID = base.MakeOriginChainID(oid)
	} else {
		chainID = chainConstraint.ChainID
	}
	chainKey := makeChainIDKey(&chainID)

	if chainConstraint.IsOrigin() {
		if existed := trie.Update(chainKey, oid[:]); existed {
			err = fmt.Errorf("addOutputToTrie: unexpected chain origin in the state: %s", chainID.StringShort())
			return
		}
	} else {
		const assertChainRecordsConsistency = false
		if assertChainRecordsConsistency {
			// previous chain record may or may not exist
			// enforcing timestamp consistency
			if prevBin := trie.TrieReader.Get(chainKey); len(prevBin) > 0 {
				prevOutputID, err1 := base.OutputIDFromBytes(prevBin)
				util.AssertNoError(err1)
				if !oid.Timestamp().After(prevOutputID.Timestamp()) {
					err = fmt.Errorf("addOutputToTrie: chain output id violates time constraint:\n   previous: %s\n   next: %s",
						prevOutputID.StringShort(), oid.StringShort())
					return
				}
			}
		}
		trie.Update(chainKey, oid[:])
	}
	return
}

func addTxToTrie(trie *immutable.TrieUpdatable, txid *base.TransactionID, unspentOutputs *set256.Set256) (delta supplyDelta, err error) {
	var stateKey [1 + base.TransactionIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], txid[:])
	// Store unspent output indices as Set256 bitmap.
	// Use []byte{0} for empty set to avoid empty trie value (which means "not present").
	value := unspentOutputs.Bytes()
	if value == nil {
		value = []byte{0}
	}
	if trie.Update(stateKey[:], value) {
		// key should not exist
		err = fmt.Errorf("addTxToTrie: transaction key should not exist: %s", txid.StringShort())
	}
	return
}

func delTxFromTrie(trie *immutable.TrieUpdatable, txid *base.TransactionID) (err error) {
	var stateKey [1 + base.TransactionIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], txid[:])

	if !trie.Delete(stateKey[:]) {
		// key should not exist
		err = fmt.Errorf("delTxFromTrie: transaction ID key should exist: %s", txid.StringShort())
	}
	return
}

func deleteChainFromTrie(trie *immutable.TrieUpdatable, chainID base.ChainID) (delta supplyDelta, err error) {
	var stateKey [1 + base.ChainIDLength]byte
	stateKey[0] = TriePartitionChainID
	copy(stateKey[1:], chainID[:])

	if existed := trie.Delete(stateKey[:]); !existed {
		// only deleting existing chainIDs
		err = fmt.Errorf("deleteChainFromTrie: chain id does not exist: %s", chainID.String())
	}
	return
}

func makeAccountKey(id ledger.ControllerID, oid base.OutputID) []byte {
	return common.Concat([]byte{TriePartitionControllers, byte(len(id))}, id[:], oid[:])
}

func makeChainIDKey(chainID *base.ChainID) []byte {
	return common.Concat([]byte{TriePartitionChainID}, chainID[:])
}

func updateTrie(trie *immutable.TrieUpdatable, mut *Mutations, inflation ...uint64) (err error) {
	var delAmount, addAmount uint64
	var delta supplyDelta

	// bitmapCache tracks in-memory bitmap state for TX records modified during this batch.
	// This is critical because TrieUpdatable.Get() reads from the persistent (committed) state,
	// not from the buffered state. Without this, multiple DEL mutations for the same TX would
	// each read the stale original bitmap and the last write would overwrite earlier changes.
	bitmapCache := make(txBitmapCache)

	for _, m := range mut.mut {
		delta, err = m.mutate(trie, mut.GCSlot, bitmapCache)
		if err != nil {
			return
		}
		if delta.decrease {
			delAmount += delta.amount
		} else {
			addAmount += delta.amount
		}
	}
	// check the main ledger invariant: number of base tokens
	if len(inflation) == 0 {
		// len(inflation) == 0 is used only in UTXODB because there is no slot inflation there
		// relax assertion
		if delAmount > addAmount {
			err = fmt.Errorf("updateTrie: major inconsistency. Deleted amount(%s) cannot be greater that the added amount(%s). Diff: %s",
				util.Th(delAmount), util.Th(addAmount), util.Th(int(addAmount)-int(delAmount)))
		}
	} else {
		if addAmount != delAmount+inflation[0] {
			err = fmt.Errorf("updateTrie: major inconsistency. Mismatch input amount(%s) + inflation(%s) != output amount(%s). Diff: %s",
				util.Th(delAmount), util.Th(inflation[0]), util.Th(addAmount), util.Th(int(addAmount)-int(delAmount+inflation[0])))
		}
	}
	return
}
