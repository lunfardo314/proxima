package tests

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/utxodb"
	"github.com/lunfardo314/proxima/sequencer"
	"github.com/lunfardo314/proxima/util/testutil"
	"github.com/stretchr/testify/require"
)

// TestZoneMilestonesLiveTiming runs v3 sequencers at the live network's timing (80 ms ticks, sequencer
// pace 6, pulse 12) and leaves the outcome of the pre-branch zone milestones in the SLOT STATS lines of
// the log. A zone milestone is issued only when it raises the classical coverage, which counts only
// outputs of the baseline state, so the load is ten independent wallets, each spending its committed
// output once per slot at its own tick, spread up to the zone, each with a tag-along to a sequencer in
// turn: new coverage keeps arriving late in the slot, as it does on the live network. Long and
// timing-sensitive, so it runs only with PROXIMA_ZONE_TEST set.
func TestZoneMilestonesLiveTiming(t *testing.T) {
	if os.Getenv("PROXIMA_ZONE_TEST") == "" {
		t.Skip("set PROXIMA_ZONE_TEST to run")
	}
	const (
		nSequencers = 6
		nWallets    = 10
		runSlots    = 25
		walletFund  = 1_000 * base.PROX
		spendAmount = 10 * base.PROX
		tagAlongFee = 10 * base.PROX
	)
	extraLedgerOptions = []ledger.ParametersOption{
		ledger.WithTickDuration(80 * time.Millisecond),
		ledger.WithTransactionPace(12),
		ledger.WithTransactionPaceSequencer(6),
	}
	// two slots of the 80 ms ledger: the boot loop starts past slot 1
	bootDelayStart = 2 * 128 * 80 * time.Millisecond
	t.Cleanup(func() {
		bootDelayStart = 3 * time.Second
		extraLedgerOptions = nil
		reinitTestLedger()
	})
	testData := initMultiSequencerTest(t, nSequencers, true)
	require.EqualValues(t, 119, ledger.L(base.MaxSlot).LastNonBranchTick())

	tagAlongSeqIDs := []base.ChainID{testData.bootstrapChainID}
	for _, o := range testData.chainOrigins {
		tagAlongSeqIDs = append(tagAlongSeqIDs, o.ChainID)
	}

	// fund the wallets from the faucet in one chain of transfers, the last one carrying a tag-along so
	// that a sequencer pulls the whole chain into the ledger
	keys := make([]ed25519.PrivateKey, nWallets)
	outs := make([]*ledger.OutputWithID, nWallets)
	faucetAddr := ledger.SigLockFromED25519PrivateKey(testData.privKeyFaucet)
	remainder := testData.faucetOutput
	for k := range keys {
		keys[k] = testutil.GetTestingPrivateKey(20000 + k)
		addr := ledger.SigLockFromED25519PrivateKey(keys[k])
		ts := base.MaximumTime(ledger.TimeNow(), remainder.Timestamp().AddTicks(int(ledger.L(base.MaxSlot).TransactionPace)))
		td := utxodb.NewTransferData(testData.privKeyFaucet, faucetAddr, ts).
			MustWithInputs(remainder).WithTargetLock(addr).WithAmount(walletFund)
		if k == nWallets-1 {
			td.WithTagAlong(testData.bootstrapChainID, tagAlongFee)
		}
		var txBytes []byte
		var err error
		txBytes, remainder, err = utxodb.MakeSimpleTransferTransactionWithRemainder(td)
		require.NoError(t, err)
		outs[k] = walletOutput(t, txBytes, addr)
		_, err = testData.wrk.TxBytesInForTests(txBytes)
		require.NoError(t, err)
	}

	testData.sequencers = make([]testSequencer, len(testData.chainOrigins))
	for i := range testData.sequencers {
		var err error
		testData.sequencers[i], err = newTestSequencerOfVersion("v3", testData.wrk, testData.chainOrigins[i].ChainID, testData.privKeyAux,
			sequencer.WithName(fmt.Sprintf("seq%d", i)),
			sequencer.WithPace(12),
			sequencer.WithProduceBranches,
		)
		require.NoError(t, err)
		testData.sequencers[i].Start()
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(runSlots)*ledger.SlotDuration())
	defer cancel()
	// wallet k spends once per slot at tick 8+10k (8..98): the last spends land just before the zone
	for k := range keys {
		go func(k int) {
			addr := ledger.SigLockFromED25519PrivateKey(keys[k])
			for slot := ledger.TimeNow().Slot + 2; ; slot++ {
				ts := base.T(slot, uint8(8+10*k))
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Until(ledger.ClockTime(ts))):
				}
				td := utxodb.NewTransferData(keys[k], addr, ts).
					MustWithInputs(outs[k]).WithTargetLock(addr).WithAmount(spendAmount).
					WithTagAlong(tagAlongSeqIDs[(int(slot)+k)%len(tagAlongSeqIDs)], tagAlongFee)
				txBytes, rem, err := utxodb.MakeSimpleTransferTransactionWithRemainder(td)
				require.NoError(t, err)
				if _, err = testData.wrk.TxBytesInForTests(txBytes); err == nil {
					outs[k] = rem
				}
			}
		}(k)
	}
	<-ctx.Done()
	for _, seq := range testData.sequencers {
		seq.Stop()
	}
	testData.bootstrapSeq.Stop()
	testData.stopAndWait(10 * time.Second)
}

// walletOutput is the output of the transfer locked to addr
func walletOutput(t *testing.T, txBytes []byte, addr ledger.SigLock) *ledger.OutputWithID {
	tx, err := transaction.ParseWithPartialValidation(txBytes)
	require.NoError(t, err)
	for _, o := range tx.ProducedOutputs() {
		if o.Output.Lock().String() == addr.String() {
			return o
		}
	}
	t.Fatalf("no output locked to %s", addr.String())
	return nil
}
