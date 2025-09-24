package multistate

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	mutationCmd interface {
		mutate(trie *immutable.TrieUpdatable) (delta supplyDelta, err error)
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
		ID              base.TransactionID
		TimeSlot        uint32
		LastOutputIndex byte
	}

	mutationDelChain struct {
		ChainID base.ChainID
	}

	Mutations struct {
		mut []mutationCmd
	}
)

func (m *mutationDelOutput) mutate(trie *immutable.TrieUpdatable) (delta supplyDelta, err error) {
	return deleteOutputFromTrie(trie, m.ID)
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

func (m *mutationAddOutput) mutate(trie *immutable.TrieUpdatable) (delta supplyDelta, err error) {
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

func (m *mutationAddTx) mutate(trie *immutable.TrieUpdatable) (delta supplyDelta, err error) {
	return addTxToTrie(trie, &m.ID, m.TimeSlot, m.LastOutputIndex)
}

func (m *mutationAddTx) text() string {
	return fmt.Sprintf("ADDTX %s : slot %d", m.ID.StringShort(), m.TimeSlot)
}

func (m *mutationAddTx) sortOrder() byte {
	return 2
}

func (m *mutationAddTx) timestamp() base.LedgerTime {
	return m.ID.Timestamp()
}

func (m *mutationDelChain) mutate(trie *immutable.TrieUpdatable) (delta supplyDelta, err error) {
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

func (mut *Mutations) InsertDelOutputMutation(id base.OutputID) {
	mut.mut = append(mut.mut, &mutationDelOutput{ID: id})
}

func (mut *Mutations) InsertAddTxMutation(id base.TransactionID, slot uint32, lastOutputIndex byte) {
	mut.mut = append(mut.mut, &mutationAddTx{
		ID:              id,
		TimeSlot:        slot,
		LastOutputIndex: lastOutputIndex,
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

func deleteOutputFromTrie(trie *immutable.TrieUpdatable, oid base.OutputID) (delta supplyDelta, err error) {
	var stateKey [1 + base.OutputIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], oid[:])

	oData := trie.Get(stateKey[:])
	if len(oData) == 0 {
		err = fmt.Errorf("deleteOutputFromTrie: output not found: %s", oid.StringShort())
		return
	}

	o, err := ledger.OutputFromBytes(oData)
	util.AssertNoError(err)

	delta.decrease = true
	delta.amount = o.TokenBalance()

	var existed bool
	existed = trie.Delete(stateKey[:])
	util.Assertf(existed, "deleteOutputFromTrie: inconsistency while deleting output %s", oid.StringShort())

	for _, accountable := range o.Lock().Accounts() {
		existed = trie.Delete(makeAccountKey(accountable.AccountID(), oid))
		// must exist
		util.Assertf(existed, "deleteOutputFromTrie: account record for %s wasn't found as expected: output %s", accountable.String(), oid.StringShort())
	}
	return
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
	for _, accountable := range out.Lock().Accounts() {
		if trie.Update(makeAccountKey(accountable.AccountID(), oid), []byte{0xff}) {
			// key should not exist
			err = fmt.Errorf("addOutputToTrie: index key should not exist: %s", oid.StringShort())
			return
		}
	}
	chainConstraint, _ := out.ChainConstraint()
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

func addTxToTrie(trie *immutable.TrieUpdatable, txid *base.TransactionID, slot uint32, lastOutputIndex byte) (delta supplyDelta, err error) {
	if strings.Contains(txid.String(), "ff40fe") {
		println()
	}
	var stateKey [1 + base.TransactionIDLength]byte
	stateKey[0] = TriePartitionLedgerState
	copy(stateKey[1:], txid[:])
	if trie.Update(stateKey[:], base.Slot2Bytes(slot)) {
		// key should not exist
		err = fmt.Errorf("addTxToTrie: transaction key should not exist: %s", txid.StringShort())
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

func makeAccountKey(id ledger.AccountID, oid base.OutputID) []byte {
	return common.Concat([]byte{TriePartitionAccounts, byte(len(id))}, id[:], oid[:])
}

func makeChainIDKey(chainID *base.ChainID) []byte {
	return common.Concat([]byte{TriePartitionChainID}, chainID[:])
}

func updateTrie(trie *immutable.TrieUpdatable, mut *Mutations, inflation ...uint64) (err error) {
	var delAmount, addAmount uint64
	var delta supplyDelta

	for _, m := range mut.mut {
		delta, err = m.mutate(trie)
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
