package multistate

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	mutationCmd interface {
		mutate(trie *immutable.TrieUpdatable) error
		text() string
		sortOrder() byte
	}

	mutationAddOutput struct {
		ID     core.OutputID
		Output *core.Output // nil means delete
	}

	mutationDelOutput struct {
		ID core.OutputID
	}

	mutationAddTx struct {
		ID       core.TransactionID
		TimeSlot core.TimeSlot
	}

	Mutations struct {
		mut []mutationCmd
	}
)

func (m *mutationDelOutput) mutate(trie *immutable.TrieUpdatable) error {
	return deleteOutputFromTrie(trie, &m.ID)
}

func (m *mutationDelOutput) text() string {
	return fmt.Sprintf("DEL   %s", m.ID.Short())
}

func (m *mutationDelOutput) sortOrder() byte {
	return 0
}

func (m *mutationAddOutput) mutate(trie *immutable.TrieUpdatable) error {
	return addOutputToTrie(trie, &m.ID, m.Output)
}

func (m *mutationAddOutput) text() string {
	return fmt.Sprintf("ADD   %s", m.ID.Short())
}

func (m *mutationAddOutput) sortOrder() byte {
	return 1
}

func (m *mutationAddTx) mutate(trie *immutable.TrieUpdatable) error {
	return addTxToTrie(trie, &m.ID, m.TimeSlot)
}

func (m *mutationAddTx) text() string {
	return fmt.Sprintf("ADDTX %s : slot %d", m.ID.Short(), m.TimeSlot)
}

func (m *mutationAddTx) sortOrder() byte {
	return 2
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

func (mut *Mutations) InsertAddOutputMutation(id core.OutputID, o *core.Output) {
	mut.mut = append(mut.mut, &mutationAddOutput{
		ID:     id,
		Output: o.Clone(),
	})
}

func (mut *Mutations) InsertDelOutputMutation(id core.OutputID) {
	mut.mut = append(mut.mut, &mutationDelOutput{ID: id})
}

func (mut *Mutations) InsertAddTxMutation(id core.TransactionID, slot core.TimeSlot) {
	mut.mut = append(mut.mut, &mutationAddTx{ID: id, TimeSlot: slot})
}

func (mut *Mutations) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, m := range mut.mut {
		ret.Add(m.text())
	}
	return ret
}

func deleteOutputFromTrie(trie *immutable.TrieUpdatable, oid *core.OutputID) error {
	var stateKey [1 + core.OutputIDLength]byte
	stateKey[0] = PartitionLedgerState
	copy(stateKey[1:], oid[:])

	oData := trie.Get(stateKey[:])
	if len(oData) == 0 {
		return fmt.Errorf("deleteOutputFromTrie: output not found: %s", oid.Short())
	}

	o, err := core.OutputFromBytesReadOnly(oData)
	util.AssertNoError(err)

	var existed bool
	existed = trie.Delete(stateKey[:])
	util.Assertf(existed, "deleteOutputFromTrie: inconsistency while deleting output %s", oid.Short())

	for _, accountable := range o.Lock().Accounts() {
		existed = trie.Delete(makeAccountKey(accountable.AccountID(), oid))
		// must exist
		util.Assertf(existed, "deleteOutputFromTrie: account record for %s wasn't found as expected: output %s", accountable.String(), oid.Short())
	}
	return nil
}

func addOutputToTrie(trie *immutable.TrieUpdatable, oid *core.OutputID, out *core.Output) error {
	var stateKey [1 + core.OutputIDLength]byte
	stateKey[0] = PartitionLedgerState
	copy(stateKey[1:], oid[:])
	if trie.Update(stateKey[:], out.Bytes()) {
		// key should not exist
		return fmt.Errorf("addOutputToTrie: UTXO key should not exist: %s", oid.Short())
	}
	for _, accountable := range out.Lock().Accounts() {
		if trie.Update(makeAccountKey(accountable.AccountID(), oid), []byte{0xff}) {
			// key should not exist
			return fmt.Errorf("addOutputToTrie: index key should not exist: %s", oid.Short())
		}
	}
	chainConstraint, _ := out.ChainConstraint()
	if chainConstraint == nil {
		// not a chain output
		return nil
	}
	// update chain output records
	var chainID core.ChainID
	if chainConstraint.IsOrigin() {
		chainID = core.OriginChainID(oid)
	} else {
		chainID = chainConstraint.ID
	}
	trie.Update(makeChainIDKey(&chainID), oid[:])
	// TODO terminating the chain
	return nil
}

func addTxToTrie(trie *immutable.TrieUpdatable, txid *core.TransactionID, slot core.TimeSlot) error {
	var stateKey [1 + core.TransactionIDLength]byte
	stateKey[0] = PartitionCommittedTransactionID
	copy(stateKey[1:], txid[:])

	if trie.Update(stateKey[:], slot.Bytes()) {
		// key should not exist
		return fmt.Errorf("addTxToTrie: transaction key should not exist: %s", txid.Short())
	}
	return nil
}

func makeAccountKey(id core.AccountID, oid *core.OutputID) []byte {
	return common.ConcatBytes([]byte{PartitionAccounts, byte(len(id))}, id[:], oid[:])
}

func makeChainIDKey(chainID *core.ChainID) []byte {
	return common.ConcatBytes([]byte{PartitionChainID}, chainID[:])
}

func UpdateTrie(trie *immutable.TrieUpdatable, mut *Mutations) (err error) {
	for _, m := range mut.mut {
		if err = m.mutate(trie); err != nil {
			return
		}
	}
	return
}
