package node_cmd

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
)

func initKillChainCmd() *cobra.Command {
	deleteChainCmd := &cobra.Command{
		Use:     "killchain <chain id>",
		Aliases: []string{"endchain, delchain"},
		Short:   `ends a chain by destroying chain output. All tokens are converted into the addressED25519-locked output with the same controlling private key`,
		Args:    cobra.ExactArgs(1),
		Run:     runKillChainCmd,
	}
	deleteChainCmd.InitDefaultHelpCmd()

	return deleteChainCmd
}

func runKillChainCmd(_ *cobra.Command, args []string) {
	//cmd.DebugFlags()
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	walletData := glb.GetWalletData()

	var tagAlongSeqID base.ChainID
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")
	clnt := glb.GetClient()

	pTagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(pTagAlongSeqID != nil, "tag-along sequencer not specified")
	tagAlongSeqID = *pTagAlongSeqID

	md, err := clnt.GetSequencerData(tagAlongSeqID)
	glb.AssertNoError(err)

	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}

	prompt := fmt.Sprintf("discontinue chain %s?", chainID.String())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}
	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())

	wg.Add(2)
	go func() {
		checkChainLoop(chainID, 2*time.Second, ctx)
		cancel()
		wg.Done()
	}()

	time.Sleep(500 * time.Millisecond)

	go func() {
		makeTransactionLoop(killChainParams{
			chainID:       chainID,
			privateKey:    walletData.PrivateKey,
			tagAlongSeqID: tagAlongSeqID,
			tagAlongFee:   feeAmount,
			repeatPeriod:  2 * time.Second,
			ctx:           ctx,
		})
		cancel()
		wg.Done()
	}()
	wg.Wait()
}

type killChainParams struct {
	chainID       base.ChainID
	privateKey    ed25519.PrivateKey
	tagAlongSeqID base.ChainID
	tagAlongFee   uint64
	repeatPeriod  time.Duration
	ctx           context.Context
}

// checkChainLoop polls chain in the LRB state and exits when chain does not exist anymore
func checkChainLoop(chainID base.ChainID, repeatPeriod time.Duration, ctx context.Context) {
	clnt := glb.GetClient()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_, _, _, err := clnt.GetChainOutput(chainID)
		if errors.Is(err, multistate.ErrNotFound) {
			glb.Infof("chain %s does not exist anymore", chainID.String())
			return
		}
		glb.AssertNoError(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(repeatPeriod):
		}

	}
}

// makeTransactionLoop periodically issues new killchain transaction for each new LRB which has new delegation output
// the transaction's timestamp is at the nearest liquidity window timestamp.
// Multiple transactions are issued until one succeeds. The rest are double-spends and are orphaned
func makeTransactionLoop(par killChainParams) {
	clnt := glb.GetClient()
	consumedOutputs := set.New[base.OutputID]()

	attempt := 1
	for {
		o, _, lrbid, err := clnt.GetChainOutput(par.chainID)
		if errors.Is(err, multistate.ErrNotFound) {
			_, lrbid, err = clnt.GetLatestReliableBranch()
			glb.AssertNoError(err)
			glb.Infof("chain %s not found. LRB (latest reliable branch) is %s (%d slots behind from now)",
				par.chainID.StringShort(), lrbid.String(), ledger.TimeNow().Slot-lrbid.Slot())
			return
		}
		glb.AssertNoError(err)

		if ledger.TimeNow().Slot-lrbid.Slot() > 2 {
			glb.Infof("warning: LRB  (latest reliable branch) is %d slots behind from now. Node may not be synced", ledger.TimeNow().Slot-lrbid.Slot())
		}
		if !consumedOutputs.Contains(o.ID) {
			ts := ledger.NextClosedDelegationTimestamp(par.chainID, o.Timestamp())

			tx, err := txbuilder.MakeEndChainTransaction(txbuilder.EndChainParams{
				Timestamp:     ts,
				ChainIn:       o,
				PrivateKey:    par.privateKey,
				TagAlongSeqID: par.tagAlongSeqID,
				TagAlongFee:   par.tagAlongFee,
			})
			glb.AssertNoError(err)

			err = clnt.SubmitTransaction(tx.Bytes())
			glb.AssertNoError(err)

			err = clnt.SubmitTransaction(tx.Bytes())
			glb.AssertNoError(err)

			lrbBehindTicks := base.DiffTicks(lrbid.Timestamp(), ledger.TimeNow())
			glb.Infof("attempt #%d. Submitted transaction %s. LRB (latest reliable branch) is %d ticks, %d slots, %v behind. ",
				attempt, tx.IDString(), lrbBehindTicks, lrbBehindTicks/256, time.Duration(lrbBehindTicks)*ledger.TickDuration())
			glb.Verbosef("-------------- transaction --------------\n%s", tx.String())

			consumedOutputs.Insert(o.ID)
			attempt++
		}

		select {
		case <-par.ctx.Done():
			return
		case <-time.After(par.repeatPeriod):
		}
	}

}
