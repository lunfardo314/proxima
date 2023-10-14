package multistate

import (
	"fmt"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/lunfardo314/unitrie/common"
	"github.com/lunfardo314/unitrie/immutable"
)

type (
	mutationCmd interface {
		Mutate(trie *immutable.TrieUpdatable) error
		String() string
	}

	mutationCmdAddOutput struct {
		ID     core.OutputID
		Output *core.Output // nil means delete
	}

	mutationCmdDelOutput struct {
		ID core.OutputID
	}

	mutationCmdAddTx struct {
		ID       core.TransactionID
		TimeSlot core.TimeSlot
	}

	Mutations struct {
		mut []mutationCmd
	}

	UpdateCmd struct {
		ID     *core.OutputID
		Output *core.Output // nil means delete
	}
)

func (m *mutationCmdAddOutput) Mutate(trie *immutable.TrieUpdatable) error {
	return addOutputToTrie(trie, &m.ID, m.Output)
}

func (m *mutationCmdAddOutput) String() string {
	return fmt.Sprintf("ADD   %s", m.ID.Short())
}

func (m *mutationCmdDelOutput) Mutate(trie *immutable.TrieUpdatable) error {
	return deleteOutputFromTrie(trie, &m.ID)
}

func (m *mutationCmdDelOutput) String() string {
	return fmt.Sprintf("DEL   %s", m.ID.Short())
}

func (m *mutationCmdAddTx) Mutate(trie *immutable.TrieUpdatable) error {
	return addTxToTrie(trie, &m.ID, m.TimeSlot)
}

func (m *mutationCmdAddTx) String() string {
	return fmt.Sprintf("ADDTX %s", m.ID.Short())
}

func (mut *Mutations) NewMutations() *Mutations {
	return &Mutations{
		mut: make([]mutationCmd, 0),
	}
}

func (mut *Mutations) InsertAddOutputMutation(id core.OutputID, o *core.Output) {
	mut.mut = append(mut.mut, &mutationCmdAddOutput{
		ID:     id,
		Output: o.Clone(),
	})
}

func (mut *Mutations) InsertDelOutputMutation(id core.OutputID) {
	mut.mut = append(mut.mut, &mutationCmdDelOutput{ID: id})
}

func (mut *Mutations) InsertAddTxMutation(id core.TransactionID) {
	mut.mut = append(mut.mut, &mutationCmdAddTx{ID: id})
}

func (mut *Mutations) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	for _, m := range mut.mut {
		ret.Add(m.String())
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
	stateKey[0] = PartitionTransactionID
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

// BalanceOnLock returns balance and number of outputs
func BalanceOnLock(rdr general.StateIndexReader, account core.Accountable) (uint64, int) {
	oDatas, err := rdr.GetUTXOsLockedInAccount(account.AccountID())
	util.AssertNoError(err)

	balance := uint64(0)
	num := 0
	for _, od := range oDatas {
		o, err := od.Parse()
		util.AssertNoError(err)
		balance += o.Output.Amount()
		num++
	}
	return balance, num
}

func BalanceOnChainOutput(rdr general.StateIndexReader, chainID *core.ChainID) uint64 {
	oData, err := rdr.GetUTXOForChainID(chainID)
	if err != nil {
		return 0
	}
	o, _, err := oData.ParseAsChainOutput()
	util.AssertNoError(err)
	return o.Output.Amount()
}

func UpdateTrie(trie *immutable.TrieUpdatable, mut *Mutations) (err error) {
	for _, m := range mut.mut {
		if err = m.Mutate(trie); err != nil {
			return
		}
	}
	return
}

//======================================================================================

// UpdateCommandsToLines
// Deprecated
func UpdateCommandsToLines(cmds []UpdateCmd, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	cmdStr := ""
	for i := range cmds {
		if cmds[i].Output == nil {
			cmdStr = "DEL"
		} else {
			cmdStr = "ADD"
		}
		ret.Add("%d : %s <- %s", i, cmds[i].ID.Short(), cmdStr)
	}
	return ret
}

// UpdateTrieOld
// Deprecated
func UpdateTrieOld(trie *immutable.TrieUpdatable, commands []UpdateCmd) error {
	var err error
	for i := range commands {
		if commands[i].Output != nil {
			err = addOutputToTrie(trie, commands[i].ID, commands[i].Output)
		} else {
			err = deleteOutputFromTrie(trie, commands[i].ID)
		}
		if err != nil {
			return fmt.Errorf("UpdateTrieOld: %v", err)
		}
	}
	return nil
}
