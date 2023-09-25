package api

import (
	"math"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type spammerConfig struct {
	scenario          string
	minBalance        uint64
	outputAmount      uint64
	bundleSize        int
	pace              int
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

	spamCmd.PersistentFlags().Uint64("spammer.minimum_balance", 1000, "minimum balance on wallet account")
	err = viper.BindPFlag("spammer.minimum_balance", spamCmd.PersistentFlags().Lookup("spammer.minimum_balance"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.output_amount", 500, "amount on the output")
	err = viper.BindPFlag("spammer.output_amount", spamCmd.PersistentFlags().Lookup("spammer.output_amount"))
	glb.AssertNoError(err)

	spamCmd.PersistentFlags().Uint64("spammer.bundle_size", 30, "transaction bundle size")
	err = viper.BindPFlag("spammer.bundle_size", spamCmd.PersistentFlags().Lookup("spammer.bundle_size"))
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
	ret.scenario = sub.GetString("scenario")
	ret.minBalance = sub.GetUint64("scenario")
	ret.outputAmount = sub.GetUint64("output_amount")
	ret.bundleSize = sub.GetInt("bundle_size")
	ret.pace = sub.GetInt("pace")
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
	glb.Infof("minimum wallet balance: %d", cfg.minBalance)
	glb.Infof("output amount: %d", cfg.outputAmount)
	glb.Infof("bundle size: %d", cfg.bundleSize)
	glb.Infof("pace: %d", cfg.pace)
	glb.Infof("max transactions: %d", cfg.maxTransactions)
	glb.Infof("max duration: %v", cfg.maxDuration)
	glb.Infof("tag-along sequencer: %s", cfg.tagAlongSequencer.String())
	glb.Infof("tag-along fee: %d", cfg.tagAlongFee)

	walletData := glb.GetWalletData()
	glb.Infof("source account (wallet): %s", walletData.Account.String())
	glb.Infof("target account: %s", cfg.target.String())

	glb.Assertf(glb.YesNoPrompt("start spamming?", false), "exit")

	return cfg
}

func runSpamCmd(_ *cobra.Command, args []string) {
	//cfg, walletData, target :=
	cfg := displaySpammerConfig()

	switch cfg.scenario {
	case "standard":
		standardScenario(cfg)
	default:
		standardScenario(cfg)
	}
}

func standardScenario(cfg spammerConfig) {
	walletData := glb.GetWalletData()

	txCounter := 0
	deadline := time.Unix(0, math.MaxInt64)
	if cfg.maxDuration > 0 {
		deadline = time.Now().Add(cfg.maxDuration)
	}

	for {
		time.Sleep(time.Duration(cfg.pace) * core.TimeTickDuration())

		glb.Assertf(cfg.maxTransactions == 0 || txCounter < cfg.maxTransactions, "maximum transaction limit %d has been reached", cfg.maxTransactions)
		glb.Assertf(time.Now().Before(deadline), "spam duration limit has been reached")

		nowisTs := core.LogicalTimeNow()
		balance, numOuts, err := getClient().GetTransferableBalance(walletData.Account, nowisTs)
		glb.AssertNoError(err)

		glb.Infof("transferable balance: %s", util.GoThousands(balance))
		requiredBalance := cfg.minBalance + cfg.outputAmount*uint64(cfg.bundleSize) + cfg.tagAlongFee
		if balance < requiredBalance {
			glb.Infof("transferable balance (%s) is too small for the bundle (required is %s). Waiting for more..",
				util.GoThousands(balance), util.GoThousands(requiredBalance))
			continue
		}

		bundle, oid := prepareBundle(walletData, numOuts > 1)
		c := getClient()
		for _, txBytes := range bundle {
			err = c.SubmitTransaction(txBytes)
			glb.AssertNoError(err)
		}
		glb.Infof("%d transactions submitted", len(bundle))
		if err = c.WaitOutputFinal(oid, core.TimeSlotDuration()*2); err != nil {
			glb.Infof("%v", err)
		}
	}
}

func prepareBundle(walletData glb.WalletData, compact bool) ([][]byte, *core.OutputID) {
	txCtx, err := getClient().CompactED25519Outputs(walletData.PrivateKey, nil, 0)
	glb.AssertNoError(err)

	if txCtx != nil {
		_, err = txCtx.ProducedOutput(0)
		glb.AssertNoError(err)
	}
	return nil, nil
}
