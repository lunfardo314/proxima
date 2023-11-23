package api

import (
	"math"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi_old/glb"
	"github.com/lunfardo314/proxima/transaction"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type spammerConfig struct {
	scenario          string
	outputAmount      uint64
	bundleSize        int
	pace              int
	submitNowait      bool
	maxTransactions   int
	maxDuration       time.Duration
	tagAlongSequencer core.ChainID
	tagAlongFee       uint64
	target            core.Accountable
}

func initSpamCmd(apiCmd *cobra.Command) {
	spamCmd := &cobra.Command{
		Use:   "spam",
		Short: `spams the ledger according to spammer.scenario`,
		Args:  cobra.NoArgs,
		Run:   runSpamCmd,
	}

	spamCmd.PersistentFlags().String("spammer.scenario", "default", "spamming scenario")
	err := viper.BindPFlag("spammer.scenario", spamCmd.PersistentFlags().Lookup("spammer.scenario"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Int("spammer.max_transactions", 0, "number of transaction limit")
	err = viper.BindPFlag("spammer.max_transactions", spamCmd.PersistentFlags().Lookup("spammer.max_transactions"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Int("spammer.max_duration_minutes", 0, "time limit in minutes")
	err = viper.BindPFlag("spammer.max_duration_minutes", spamCmd.PersistentFlags().Lookup("spammer.max_duration_minutes"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.output_amount", 500, "amount on the output")
	err = viper.BindPFlag("spammer.output_amount", spamCmd.PersistentFlags().Lookup("spammer.output_amount"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.bundle_size", 30, "transaction bundle size")
	err = viper.BindPFlag("spammer.bundle_size", spamCmd.PersistentFlags().Lookup("spammer.bundle_size"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.pace", 3, "pace in ticks")
	err = viper.BindPFlag("spammer.pace", spamCmd.PersistentFlags().Lookup("spammer.pace"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Bool("spammer.submit_nowait", false, "submit transaction without waiting for validation result")
	err = viper.BindPFlag("spammer.submit_nowait", spamCmd.PersistentFlags().Lookup("spammer.submit_nowait"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().String("spammer.tag_along.sequencer", "", "spammer.tag_along.sequencer")
	err = viper.BindPFlag("spammer.tag_along.sequencer", spamCmd.PersistentFlags().Lookup("spammer.tag_along.sequencer"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.tag_along.fee", 500, "spammer.tag_along.sequencer")
	err = viper.BindPFlag("spammer.tag_along.fee", spamCmd.PersistentFlags().Lookup("spammer.tag_along.fee"))
	glb.AssertNoError(err)

	apiCmd.AddCommand(spamCmd)
}

func readSpammerConfigIn(sub *viper.Viper) (ret spammerConfig) {
	glb.Assertf(sub != nil, "spammer configuration unavailable")
	ret.scenario = sub.GetString("scenario")
	ret.outputAmount = sub.GetUint64("output_amount")
	ret.bundleSize = sub.GetInt("bundle_size")
	ret.pace = sub.GetInt("pace")
	ret.submitNowait = sub.GetBool("submit_nowait")
	ret.maxTransactions = sub.GetInt("max_transactions")
	ret.maxDuration = time.Duration(sub.GetInt("max_duration_minutes")) * time.Minute
	ret.tagAlongFee = sub.GetUint64("tag_along.fee")
	seqStr := sub.GetString("tag_along.sequencer")
	var err error
	ret.tagAlongSequencer, err = core.ChainIDFromHexString(seqStr)
	glb.AssertNoError(err)
	ret.target, err = core.AddressED25519FromSource(sub.GetString("target"))
	glb.AssertNoError(err)
	return
}

func displaySpammerConfig() spammerConfig {
	cfg := readSpammerConfigIn(viper.Sub("spammer"))
	glb.Infof("scenario: %s", cfg.scenario)
	glb.Infof("output amount: %d", cfg.outputAmount)
	glb.Infof("bundle size: %d", cfg.bundleSize)
	glb.Infof("pace: %d", cfg.pace)
	glb.Infof("submit nowait: %v", cfg.submitNowait)
	glb.Infof("max transactions: %d", cfg.maxTransactions)
	glb.Infof("max duration: %v", cfg.maxDuration)
	glb.Infof("tag-along sequencer: %s", cfg.tagAlongSequencer.String())
	glb.Infof("tag-along fee: %d", cfg.tagAlongFee)

	walletData := glb.GetWalletData()
	glb.Infof("source account (wallet): %s", walletData.Account.String())
	glb.Infof("target account: %s", cfg.target.String())

	glb.Assertf(glb.YesNoPrompt("start spamming?", true, glb.BypassYesNoPrompt()), "exit")

	return cfg
}

func runSpamCmd(_ *cobra.Command, args []string) {
	cfg := displaySpammerConfig()
	switch cfg.scenario {
	case "standard":
		standardScenario(cfg)
	default:
		standardScenario(cfg)
	}
}

const minimumBalance = 1000

func standardScenario(cfg spammerConfig) {
	walletData := glb.GetWalletData()

	txCounter := 0
	deadline := time.Unix(0, math.MaxInt64)
	if cfg.maxDuration > 0 {
		deadline = time.Now().Add(cfg.maxDuration)
	}

	beginTime := time.Now()
	for {
		time.Sleep(time.Duration(cfg.pace) * core.TimeTickDuration())

		glb.Assertf(cfg.maxTransactions == 0 || txCounter < cfg.maxTransactions, "maximum transaction limit %d has been reached", cfg.maxTransactions)
		glb.Assertf(time.Now().Before(deadline), "spam duration limit has been reached")

		nowisTs := core.LogicalTimeNow()
		outs, balance, err := getClient().GetTransferableOutputs(walletData.Account, nowisTs, cfg.bundleSize)
		glb.AssertNoError(err)

		glb.Infof("transferable balance: %s, number of outputs: %d", util.GoThousands(balance), len(outs))
		requiredBalance := minimumBalance + cfg.outputAmount*uint64(cfg.bundleSize) + cfg.tagAlongFee
		if balance < requiredBalance {
			glb.Infof("transferable balance (%s) is too small for the bundle (required is %s). Waiting for more..",
				util.GoThousands(balance), util.GoThousands(requiredBalance))

			continue
		}

		bundle, oid := prepareBundle(walletData, cfg)
		bundlePace := cfg.pace * len(bundle)
		bundleDuration := time.Duration(bundlePace) * core.TimeTickDuration()
		glb.Infof("submitting bundle of %d transactions, total duration %d ticks, %v", len(bundle), bundlePace, bundleDuration)

		c := getClient()
		for _, txBytes := range bundle {
			err = c.SubmitTransaction(txBytes, cfg.submitNowait)
			glb.AssertNoError(err)
		}
		glb.Verbosef("%d transactions submitted", len(bundle))

		glb.AssertNoError(waitForInclusion(oid))

		txCounter += len(bundle)
		timeSinceBeginning := time.Since(beginTime)
		glb.Infof("tx counter: %d, TPS avg: %2f", txCounter, float32(txCounter)/float32(timeSinceBeginning/time.Second))
	}
}

func maxTimestamp(outs []*core.OutputWithID) (ret core.LogicalTime) {
	for _, o := range outs {
		ret = core.MaxLogicalTime(ret, o.Timestamp())
	}
	return
}

func prepareBundle(walletData glb.WalletData, cfg spammerConfig) ([][]byte, core.OutputID) {
	ret := make([][]byte, 0)
	c := getClient()
	txCtx, err := c.MakeCompactTransaction(walletData.PrivateKey, nil, 0, cfg.bundleSize*3)
	glb.AssertNoError(err)

	numTx := cfg.bundleSize
	var lastOuts []*core.OutputWithID
	if txCtx != nil {
		ret = append(ret, txCtx.TransactionBytes())
		lastOut, _ := txCtx.ProducedOutput(0)
		lastOuts = []*core.OutputWithID{lastOut}
		numTx--
	} else {
		lastOuts, _, err = c.GetTransferableOutputs(walletData.Account, core.LogicalTimeNow(), cfg.bundleSize)
		glb.AssertNoError(err)
	}

	for i := 0; i < numTx; i++ {
		fee := uint64(0)
		if i == numTx-1 {
			fee = cfg.tagAlongFee
		}
		ts := core.MaxLogicalTime(maxTimestamp(lastOuts).AddTimeTicks(cfg.pace), core.LogicalTimeNow())
		txBytes, err := client.MakeTransferTransaction(client.MakeTransferTransactionParams{
			Inputs:        lastOuts,
			Target:        cfg.target.AsLock(),
			Amount:        cfg.outputAmount,
			Remainder:     walletData.Account,
			PrivateKey:    walletData.PrivateKey,
			TagAlongSeqID: &cfg.tagAlongSequencer,
			TagAlongFee:   fee,
			Timestamp:     ts,
		})
		glb.AssertNoError(err)

		ret = append(ret, txBytes)

		lastOuts, err = transaction.OutputsWithIDFromTransactionBytes(txBytes)
		glb.AssertNoError(err)
		lastOuts = util.FilterSlice(lastOuts, func(o *core.OutputWithID) bool {
			return core.EqualConstraints(o.Output.Lock(), walletData.Account)
		})
	}
	glb.Verbosef("last outputs in the bundle:")
	for i, o := range lastOuts {
		glb.Verbosef("--- %d:\n%s", i, o.String())
	}

	return ret, lastOuts[0].ID
}
