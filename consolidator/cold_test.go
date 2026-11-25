package consolidator

import (
	"crypto/ed25519"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/stretchr/testify/require"
)

// The transactions of a cold run (kb/cold_wallet.md), built by the consolidator's
// own builders and validated by the ledger: the loop needs a node, the
// transactions do not.

type coldEnv struct {
	u        *utxodb.UTXODB
	k        *Consolidator
	hotKey   ed25519.PrivateKey
	coldKey  ed25519.PrivateKey
	hot      ledger.SigLock
	cold     ledger.SigLock
	anyChain base.ChainID
}

// newColdEnv is a test ledger with a funded hot wallet holding two outputs, a cold
// wallet, and a consolidator in key mode on the two, the cold key present.
func newColdEnv(t *testing.T) *coldEnv {
	ledger.ResetForTesting()
	genesisKey := ledger.InitWithTestingLedgerData()
	u := utxodb.NewUTXODB(genesisKey, true)

	desc, err := easyfl.ReadLibraryFromJSON(easyfl.ToJSON(ledger.L(base.MaxSlot).Library, true, false))
	require.NoError(t, err)
	tlib, err := txbuildercore.NewLibrary(desc)
	require.NoError(t, err)

	privs, _, addrs := u.GenerateAddresses(0, 2)
	env := &coldEnv{u: u, hotKey: privs[0], coldKey: privs[1], hot: addrs[0], cold: addrs[1]}
	env.anyChain[0] = 1
	require.NoError(t, u.TokensFromFaucet(env.hot, 300*prox))
	require.NoError(t, u.TokensFromFaucet(env.hot, 200*prox))

	hotID, coldID := base.HolderID(env.hot), base.HolderID(env.cold)
	env.k = &Consolidator{
		consts:        ledger.L(base.MaxSlot).Constants,
		lib:           tlib,
		privateKey:    env.hotKey,
		holderID:      hotID,
		hotKey:        env.hotKey,
		hotID:         hotID,
		account:       env.hot,
		masterID:      coldID,
		cold:          &Cold{HolderID: coldID, PrivateKey: env.coldKey},
		floor:         floor,
		tagAlongSeqID: env.anyChain,
		tagAlongFee:   50 * prox, // above the storage deposit of a tag-along output
	}
	return env
}

// outputsOf is the account's outputs in the test ledger state.
func (env *coldEnv) outputsOf(t *testing.T, acc ledger.SigLock) []*ledger.OutputWithID {
	data, err := env.u.StateReader().GetUTXOsForController(acc.ControllerID())
	require.NoError(t, err)
	ret := make([]*ledger.OutputWithID, 0, len(data))
	for _, od := range data {
		o, err := od.Parse()
		require.NoError(t, err)
		ret = append(ret, o)
	}
	return ret
}

// The first transaction of a pair carries the hot inputs to one cold sigLock
// holding what moves, at output 0, and what is kept back to a hot sigLock, and
// nothing else: no tag-along, so no sequencer includes it alone. The second,
// signed by the cold key, spends that cold output while it is not yet settled
// anywhere but in this test ledger, stamped at least the transaction pace later.
func TestColdPair(t *testing.T) {
	env := newColdEnv(t)
	k := env.k
	inputs := env.outputsOf(t, env.hot)
	require.Len(t, inputs, 2)
	const moved, kept = 400 * prox, 100 * prox

	coldOut, err := k.viaCold(inputs, moved, kept)
	require.NoError(t, err)
	require.NotNil(t, k.pre)

	tx1, err := transaction.Parse(k.pre.bytes)
	require.NoError(t, err)
	require.EqualValues(t, 2, tx1.NumProducedOutputs())
	wantCold, err := txbuildercore.NewSigLockOutput(k.lib, moved, base.HolderID(env.cold))
	require.NoError(t, err)
	wantKept, err := txbuildercore.NewSigLockOutput(k.lib, kept, base.HolderID(env.hot))
	require.NoError(t, err)
	require.Equal(t, wantCold.Bytes(), tx1.MustProducedOutputAt(0).Bytes(), "output 0 is the cold sigLock holding what moves")
	require.Equal(t, wantKept.Bytes(), tx1.MustProducedOutputAt(1).Bytes(), "output 1 is the hot sigLock holding what is kept")
	require.NoError(t, env.u.AddTransaction(k.pre.bytes))
	require.EqualValues(t, coldOut.ID, base.MustNewOutputID(tx1.ID(), 0))

	// the second transaction: a move to a sequencer, the tag-along from the cold wallet
	var tx2 []byte
	k.asCold(func() []base.OutputID {
		require.Equal(t, base.HolderID(env.cold), k.holderID, "asCold signs as the cold wallet")
		txb := txbuildercore.New(k.consts.UpgradeIndex)
		_, newest, err := consumeInputs(txb, []*ledger.OutputWithID{coldOut}, 0)
		require.NoError(t, err)
		out, err := txbuildercore.NewTagAlongOutput(k.lib, moved, env.anyChain, k.holderID)
		require.NoError(t, err)
		txb.ProduceOutput(out.Bytes())
		ts := k.timestamp(newest)
		require.GreaterOrEqual(t, ts.TicksSinceGenesis(), tx1.Timestamp().TicksSinceGenesis()+int64(k.consts.TransactionPace))
		k.finish(txb, ts)
		tx2 = txb.Bytes()
		return nil
	})
	require.Equal(t, base.HolderID(env.hot), k.holderID, "the signer is the hot wallet again")
	require.NoError(t, env.u.AddTransaction(tx2))
}

// A delegation mastered by the hot key moves under the cold wallet: re-delegated
// on the master path, signed by the hot key, its successor naming the cold
// holder ID as master. The ledger pins nothing on the successor of the master path.
func TestColdMigrateDelegationMaster(t *testing.T) {
	env := newColdEnv(t)
	k := env.k

	// a delegation mastered by the hot wallet
	ts := base.MaximumTime(ledger.TimeNow(), env.outputsOf(t, env.hot)[0].Timestamp()).AddSlots(1)
	par, err := env.u.MakeTransferInputData(env.hotKey, nil, ts)
	require.NoError(t, err)
	txBytes, err := utxodb.MakeSimpleTransferTransaction(par.WithAmount(200 * prox).
		WithTargetLock(ledger.NewDelegateLock(env.anyChain, base.HolderID(env.hot), 0)).
		WithConstraint(ledger.NewChainOrigin(ts.Slot)).
		WithConstraint(ledger.DelegateLockState{}))
	require.NoError(t, err)
	require.NoError(t, env.u.AddTransaction(txBytes))

	var d *ownDelegation
	for _, o := range env.outputsOf(t, env.hot) {
		if o.Output.Lock().Name() != ledger.DelegateLockName {
			continue
		}
		wo, err := txbuildercore.OutputFromBytes(o.Output.Bytes())
		require.NoError(t, err)
		view, ok, err := k.lib.ParseDelegationOutput(wo, o.ID)
		require.NoError(t, err)
		require.True(t, ok && view.StandardLayout)
		d = &ownDelegation{view: view, oid: o.ID, bytes: o.Output.Bytes(), balance: o.Output.TokenBalance()}
	}
	require.NotNil(t, d)
	require.Equal(t, base.HolderID(env.hot), d.view.MasterID)

	// one slot of chain inflation, as the node evaluates it for the consolidator; the successor is
	// stamped in the next slot, as a live delegation is at least that old when it is moved
	lib := ledger.L(d.oid.Slot())
	res, err := lib.EvalFromSource(lib.Library.NewGlobalDataNoTrace(nil),
		fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/1)", d.balance, d.oid.Slot()))
	require.NoError(t, err)
	inflation := binary.BigEndian.Uint64(append(make([]byte, 8-len(res)), res...))

	// the steps of retargetDelegation, signed by the hot key
	txb := txbuildercore.New(k.consts.UpgradeIndex)
	k.consumeDelegation(txb, d, 0, true)
	target := txbuildercore.SequencerCandidate{ID: env.anyChain}
	require.NoError(t, k.produceDelegationSuccessor(txb, d, target, d.balance+inflation-k.tagAlongFee, inflation))
	require.NoError(t, k.produceTagAlongAndKept(txb, 0))
	k.finish(txb, d.oid.Timestamp().AddSlots(1))
	require.NoError(t, env.u.AddTransaction(txb.Bytes()))

	// the delegation now has the cold wallet as master
	var found bool
	for _, o := range env.outputsOf(t, env.cold) {
		if o.Output.Lock().Name() != ledger.DelegateLockName {
			continue
		}
		wo, err := txbuildercore.OutputFromBytes(o.Output.Bytes())
		require.NoError(t, err)
		view, ok, err := k.lib.ParseDelegationOutput(wo, o.ID)
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, d.view.ChainID, view.ChainID, "the same delegation chain")
		require.Equal(t, base.HolderID(env.cold), view.MasterID)
		found = true
	}
	require.True(t, found)
}
