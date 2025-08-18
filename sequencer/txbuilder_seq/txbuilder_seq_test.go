package txbuilder_seq

import (
	"crypto/ed25519"
	"math/rand"
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// initializes ledger.Library singleton for all tests and creates testing genesis private key

var genesisPrivateKey ed25519.PrivateKey

func init() {
	genesisPrivateKey = ledger.InitWithTestingLedgerIDData()
}

func TestBase(t *testing.T) {
	privKey := testutil.GetTestingPrivateKey()
	addr := ledger.AddressED25519FromPrivateKey(privKey)
	seqID := base.RandomChainID()
	bal := ledger.L().ID.MinimumAmountOnSequencer << 8

	sd := seqdata.New().
		SetName("test_seq").
		IncBranchHeight(2).
		IncChainHeight(4).
		SetMinimumFee(1)

	predTs := base.NewLedgerTime(1000, 50)
	predID := base.MustNewOutputID(base.RandomTransactionID(true, 2, predTs), 0)

	newPredChain := func(frozen ...int64) *ledger.OutputWithChainID {
		amounts := append(append(make([]int64, 0), int64(bal), 0), frozen...)

		predChain := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithAmounts(amounts...).WithLock(addr)
			ccIdx := o.MustPushConstraint(ledger.NewChainConstraint(seqID, 0, 2, 1000, bal).Bytes())
			_ = o.MustPushConstraint(ledger.NewSequencerConstraint(ccIdx).Bytes())
			_ = o.MustPushConstraint(easyfl.InlineDataBytecode(sd.Bytes()))
		})

		pred, ok := ledger.AsOutputWithChainID(predChain, predID)
		require.True(t, ok)
		return &pred
	}

	newTxb := func(ts base.LedgerTime, frozen ...int64) *SequencerTxBuilder {
		txb, err := New(ts, newPredChain(frozen...), nil, privKey, multistate.DummyStateReader)
		require.NoError(t, err)
		rndEndorsement := base.RandomTransactionID(true, 2, base.NewLedgerTime(ts.Slot, 0))
		err = txb.AddEndorsement(rndEndorsement)
		require.NoError(t, err)
		return txb
	}
	t.Run("+1 slot", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1 slot frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots frozen 1 epoch", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1 slot frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(1)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+2000 slots frozen 4 epochs", func(t *testing.T) {
		ts := predTs.AddSlots(2000)
		txb := newTxb(ts, 11_000_000, 11_000_000, 11_000_000, 11_000_000)
		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+100 slot tag_along", func(t *testing.T) {
		ts := predTs.AddSlots(100)
		txb := newTxb(ts)

		tagAlongOut := ledger.OutputWithID{
			ID: base.OutputID{},
			Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(1_000).WithLock(ledger.ChainLockFromChainID(seqID))
			}),
		}
		err := txb.AddTagAlongInput(&tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot tag_along", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		tagAlongOut := ledger.OutputWithID{
			ID: base.OutputID{},
			Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(1_000).WithLock(ledger.ChainLockFromChainID(seqID))
			}),
		}
		err := txb.AddTagAlongInput(&tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("+1000 slot withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		cmd := NewWithdrawCommandBytecode(privKey, 10_000_000, addr)
		tagAlongOut := ledger.OutputWithID{
			ID: base.OutputID{},
			Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(1_000).WithLock(ledger.ChainLockFromChainID(seqID))
				o.MustPushConstraint(cmd)
			}),
		}
		err := txb.AddTagAlongInput(&tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("many rnd withdraw", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		rndWithdraw := func(amount uint64, slot base.Slot) error {
			cmd := NewWithdrawCommandBytecode(privKey, amount, addr)
			tagAlongOut := ledger.OutputWithID{
				ID: base.MustNewOutputID(base.RandomTransactionID(false, 2, base.NewLedgerTime(slot, 50)), 1),
				Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
					o.WithTokenBalance(1_000).WithLock(ledger.ChainLockFromChainID(seqID))
					o.MustPushConstraint(cmd)
				}),
			}
			err := txb.AddTagAlongInput(&tagAlongOut)
			return err
		}

		const howMany = 2 // 254
		for i := 0; i < howMany; i++ {
			rnd := rand.Intn(500)
			err := rndWithdraw(10_000_000-uint64(rnd), predTs.Slot-base.Slot(rnd))
			require.NoError(t, err)
		}

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
	t.Run("change seq name", func(t *testing.T) {
		ts := predTs.AddSlots(1000)
		txb := newTxb(ts, 1_000_000, 1_000_000, 1_000_000, 1_000_000)

		predSeqData, err := ledger.ParseSequencerData(txb.chainInput.Output)
		require.NoError(t, err)

		cmd := NewSetSequencerDataCommandBytecode(privKey, predSeqData.Clone(func(sdUpdated *seqdata.SequencerData) {
			sdUpdated.SetName("newName").IncChainHeight()
		}))

		tagAlongOut := ledger.OutputWithID{
			ID: base.OutputID{},
			Output: ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(1_000).WithLock(ledger.ChainLockFromChainID(seqID))
				o.MustPushConstraint(cmd)
			}),
		}
		err = txb.AddTagAlongInput(&tagAlongOut)
		require.NoError(t, err)

		_, _, txString, err := txb.BytesWithValidation()
		t.Logf("\n--------- tx --------\n%s", txString)

		require.NoError(t, err)
	})
}
