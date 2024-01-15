package multistate

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
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
		ID     ledger.OutputID
		Output *ledger.Output // nil means delete
	}

	mutationDelOutput struct {
		ID ledger.OutputID
	}

	mutationAddTx struct {
		ID              ledger.TransactionID
		TimeSlot        ledger.Slot
		LastOutputIndex byte
	}

	Mutations struct {
		mut []mutationCmd
	}
)

func (m *mutationDelOutput) mutate(trie *immutable.TrieUpdatable) error {
	return deleteOutputFromTrie(trie, &m.ID)
}

func (m *mutationDelOutput) text() string {
	return fmt.Sprintf("DEL   %s", m.ID.StringShort())
}

func (m *mutationDelOutput) sortOrder() byte {
	return 0
}

func (m *mutationAddOutput) mutate(trie *immutable.TrieUpdatable) error {
	return addOutputToTrie(trie, &m.ID, m.Output)
}

func (m *mutationAddOutput) text() string {
	return fmt.Sprintf("ADD   %s", m.ID.StringShort())
}

func (m *mutationAddOutput) sortOrder() byte {
	return 1
}

func (m *mutationAddTx) mutate(trie *immutable.TrieUpdatable) error {
	return addTxToTrie(trie, &m.ID, m.TimeSlot, m.LastOutputIndex)
}

func (m *mutationAddTx) text() string {
	return fmt.Sprintf("ADDTX %s : slot %d", m.ID.StringShort(), m.TimeSlot)
}

func (m *mutationAddTx) sortOrder() byte {
	return 2
}

func (m *mutationAddTx) valueBytes() []byte {
	return common.ConcatBytes(m.TimeSlot.Bytes(), []byte{m.LastOutputIndex})
}

func addTxValueFromBytes(data []byte) (ledger.Slot, byte, error) {
	if len(data) != 5 {
		return 0, 0, fmt.Errorf("wrong data length")
	}
	retSlot, err := ledger.TimeSlotFromBytes(data[:4])
	if err != nil {
		return 0, 0, err
	}
	return retSlot, data[4], nil
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

func (mut *Mutations) InsertAddOutputMutation(id ledger.OutputID, o *ledger.Output) {
	mut.mut = append(mut.mut, &mutationAddOutput{
		ID:     id,
		Output: o.Clone(),
	})
}

func (mut *Mutations) InsertDelOutputMutation(id ledger.OutputID) {
	mut.mut = append(mut.mut, &mutationDelOutput{ID: id})
}

func (mut *Mutations) InsertAddTxMutation(id ledger.TransactionID, slot ledger.Slot, lastOutputIndex byte) {
	mut.mut = append(mut.mut, &mutationAddTx{
		ID:              id,
		TimeSlot:        slot,
		LastOutputIndex: lastOutputIndex,
	})
}

func (mut *Mutations) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, m := range mut.mut {
		ret.Add(m.text())
	}
	return ret
}

func deleteOutputFromTrie(trie *immutable.TrieUpdatable, oid *ledger.OutputID) error {
	var stateKey [1 + ledger.OutputIDLength]byte
	stateKey[0] = PartitionLedgerState
	copy(stateKey[1:], oid[:])

	oData := trie.Get(stateKey[:])
	if len(oData) == 0 {
		return fmt.Errorf("deleteOutputFromTrie: output not found: %s", oid.StringShort())
	}

	o, err := ledger.OutputFromBytesReadOnly(oData)
	util.AssertNoError(err)

	var existed bool
	existed = trie.Delete(stateKey[:])
	util.Assertf(existed, "deleteOutputFromTrie: inconsistency while deleting output %s", oid.StringShort())

	for _, accountable := range o.Lock().Accounts() {
		existed = trie.Delete(makeAccountKey(accountable.AccountID(), oid))
		// must exist
		util.Assertf(existed, "deleteOutputFromTrie: account record for %s wasn't found as expected: output %s", accountable.String(), oid.StringShort())
	}
	return nil
}

func addOutputToTrie(trie *immutable.TrieUpdatable, oid *ledger.OutputID, out *ledger.Output) error {
	var stateKey [1 + ledger.OutputIDLength]byte
	stateKey[0] = PartitionLedgerState
	copy(stateKey[1:], oid[:])
	if trie.Update(stateKey[:], out.Bytes()) {
		// key should not exist
		return fmt.Errorf("addOutputToTrie: UTXO key should not exist: %s", oid.StringShort())
	}
	for _, accountable := range out.Lock().Accounts() {
		if trie.Update(makeAccountKey(accountable.AccountID(), oid), []byte{0xff}) {
			// key should not exist
			return fmt.Errorf("addOutputToTrie: index key should not exist: %s", oid.StringShort())
		}
	}
	chainConstraint, _ := out.ChainConstraint()
	if chainConstraint == nil {
		// not a chain output
		return nil
	}
	// update chain output records
	var chainID ledger.ChainID
	if chainConstraint.IsOrigin() {
		chainID = ledger.OriginChainID(oid)
	} else {
		chainID = chainConstraint.ID
	}
	chainKey := makeChainIDKey(&chainID)

	if chainConstraint.IsOrigin() {
		if existed := trie.Update(chainKey, oid[:]); existed {
			return fmt.Errorf("addOutputToTrie: unexpected chain origin in the state: %s", chainID.StringShort())
		}
	} else {
		const assertChainRecordsConsistency = true
		if assertChainRecordsConsistency {
			// previous chain record may or may not exist
			// enforcing timestamp consistency
			if prevBin := trie.TrieReader.Get(chainKey); len(prevBin) > 0 {
				prevOutputID, err := ledger.OutputIDFromBytes(prevBin)
				util.AssertNoError(err)
				if !oid.Timestamp().After(prevOutputID.Timestamp()) {
					fmt.Println("breakpoint")
				}
				util.Assertf(oid.Timestamp().After(prevOutputID.Timestamp()),
					"addOutputToTrie: chain output ID violates time constraint:\n   previous: %s\n   next: %s",
					func() any { return prevOutputID.StringShort() },
					func() any { return oid.StringShort() },
				)
			}
		}
		trie.Update(chainKey, oid[:])
	}

	// TODO terminating the chain
	return nil
}

func addTxToTrie(trie *immutable.TrieUpdatable, txid *ledger.TransactionID, slot ledger.Slot, lastOutputIndex byte) error {
	var stateKey [1 + ledger.TransactionIDLength]byte
	stateKey[0] = PartitionCommittedTransactionID
	copy(stateKey[1:], txid[:])

	if trie.Update(stateKey[:], slot.Bytes()) {
		// key should not exist
		return fmt.Errorf("addTxToTrie: transaction key should not exist: %s", txid.StringShort())
	}
	return nil
}

func makeAccountKey(id ledger.AccountID, oid *ledger.OutputID) []byte {
	return common.ConcatBytes([]byte{PartitionAccounts, byte(len(id))}, id[:], oid[:])
}

func makeChainIDKey(chainID *ledger.ChainID) []byte {
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
