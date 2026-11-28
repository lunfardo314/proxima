package delegationpool

import (
	"testing"

	"github.com/lunfardo314/proxima/examples/exhelp"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// scanTestState is a ledger state with delegations to target and to another sequencer, produced at
// chosen slots, and an output of the master that names target in its index values.
type scanTestState struct {
	u                 *utxodb.UTXODB
	target, other     base.ChainID
	old, recent, away base.ChainID // delegations: old and recent to target, away to the other sequencer
	lastSlot          uint32
}

func newScanTestState(t *testing.T) *scanTestState {
	key := ledger.InitWithTestingLedgerData()
	u := utxodb.NewUTXODB(key, true)
	masterKey, _, master := u.GenerateAddress(1)
	require.NoError(t, u.TokensFromFaucet(master, 1_000_000*base.PROX))
	ret := &scanTestState{u: u, target: base.RandomChainID(), other: base.RandomChainID()}

	// delegate commits a delegation to target produced slotsAhead slots after the master's newest output
	delegate := func(target base.ChainID, slotsAhead uint32) base.ChainID {
		outs, _ := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(master, 100_000*base.PROX)
		ts := outs[0].ID.Timestamp()
		for _, o := range outs {
			ts = base.MaximumTime(ts, o.ID.Timestamp())
		}
		ts = ts.AddSlots(slotsAhead)
		txBytes, err := utxodb.MakeDelegationInitTransaction(utxodb.MakeDelegationInitTransactionParams{
			Timestamp:            ts,
			Amount:               50_000 * base.PROX,
			MasterID:             base.HolderID(master),
			Target:               target,
			RequiredInflationCut: 100,
			MasterPrivateKey:     masterKey,
			Inputs:               outs,
			TagAlongSequencer:    base.RandomChainID(),
			TagAlongFee:          500,
		})
		require.NoError(t, err)
		tx, err := transaction.Parse(txBytes)
		require.NoError(t, err)
		chainOut, err := tx.MustProducedOutputWithIDAt(0).AsChainOutput()
		require.NoError(t, err)
		require.NoError(t, u.AddTransaction(txBytes))
		ret.lastSlot = ts.Slot
		return chainOut.ChainID
	}
	ret.old = delegate(ret.target, 1)
	ret.away = delegate(ret.other, 1)
	ret.recent = delegate(ret.target, 500)

	// an output of the master naming target in its index values, in the last slot: not a delegation
	outs, _ := u.SugaredStateReader().GetOutputsLockedInAddressED25519ForAmount(master, 100*base.PROX)
	txb := exhelp.New()
	total, maxTs, err := txb.ConsumeOutputsNoUnlock(outs...)
	require.NoError(t, err)
	txb.PutSignatureUnlock(0)
	for i := 1; i < len(outs); i++ {
		require.NoError(t, txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
	}
	_, err = txb.ProduceOutput(ledger.NewOutput(func(o *ledger.OutputBuilder) {
		o.WithAmounts(int64(total)).WithLock(master)
		o.PutConstraint(ledger.IndexValuesTupleBytes([][]byte{master[:], ret.target[:]}), ledger.ConstraintIndexIndexValues)
	}))
	require.NoError(t, err)
	txb.SetTimestamp(maxTs.AddTicks(int(ledger.L(maxTs.Slot).TransactionPace)))
	txb.ComputeInputCommitment()
	txb.SignED25519(masterKey)
	require.NoError(t, u.AddTransaction(txb.Bytes(), func(_ *transaction.Transaction, e error) error { return e }))
	return ret
}

// The full discovery, run at start, finds every delegation to the sequencer. The periodic one reads
// only the known delegations by chain ID and the slots since the last discovery: it follows a known
// delegation however old, finds a new one in the window, and does not see an unknown one older than
// the window, which only the full discovery and the listener enrol.
func TestDiscoveryReadsKnownAndRecent(t *testing.T) {
	st := newScanTestState(t)
	rdr := st.u.SugaredStateReader()
	p := &DelegationPool{target: st.target, entries: map[base.ChainID]*delegationEntry{}}
	keys := func(m map[base.ChainID]*delegationEntry) (ret []base.ChainID) {
		for cid := range m {
			ret = append(ret, cid)
		}
		return
	}

	require.ElementsMatch(t, []base.ChainID{st.old, st.recent}, keys(p.discover(rdr, true, nil, 0, st.lastSlot)), "full")
	require.ElementsMatch(t, []base.ChainID{st.old, st.recent},
		keys(p.discover(rdr, false, []base.ChainID{st.old}, st.lastSlot, st.lastSlot)), "periodic, old one known")
	require.ElementsMatch(t, []base.ChainID{st.recent},
		keys(p.discover(rdr, false, nil, st.lastSlot, st.lastSlot)), "periodic, old one unknown and outside the window")
}

// The periodic foreign scan stops after the part in which its budget runs out, and successive scans
// from the cursor go round all 256 parts of the chain-ID partition. The foreign load computed with
// the scan is the one Snapshot would compute from the windows.
func TestForeignScanBounded(t *testing.T) {
	st := newScanTestState(t)
	rdr := st.u.SugaredStateReader()
	p := &DelegationPool{target: st.target, entries: map[base.ChainID]*delegationEntry{}}

	all, err := p.scanForeignParts(rdr, 0, 0)
	require.NoError(t, err)
	require.Len(t, all, 256, "budget 0 reads every part")

	var cursor byte
	covered := 0
	for covered < 256 {
		parts, err := p.scanForeignParts(rdr, cursor, 1)
		require.NoError(t, err)
		require.NotEmpty(t, parts)
		require.Less(t, len(parts), 256, "a budget of one tip stops at the first part holding a chain")
		cursor += byte(len(parts))
		covered += len(parts)
	}

	const slot = 100_000
	lib := ledger.L(slot)
	start := lib.LastSlotInEpochDirect(st.target, lib.EpochFromSlotDirect(st.target, slot, lib.DelegationEpochSlots)+3, lib.DelegationEpochSlots) + 1
	p.foreign = []foreignWindow{{start: start, amount: 1000}}
	_, direct, _ := p.Snapshot(slot, 1000)
	p.foreignLoad = p.foreignLoadByEpoch(slot)
	_, cached, _ := p.Snapshot(slot, 1000)
	require.NotEmpty(t, direct)
	require.Equal(t, direct, cached)
}
