package glb

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type WalletData struct {
	PrivateKey ed25519.PrivateKey
	Account    ledger.AddressED25519
	Sequencer  *ledger.ChainID
}

func GetWalletData() (ret WalletData) {
	ret.PrivateKey = MustGetPrivateKey()
	ret.Account = ledger.AddressED25519FromPrivateKey(ret.PrivateKey)
	ret.Sequencer = GetOwnSequencerID()
	return
}

func MustGetPrivateKey() ed25519.PrivateKey {
	ret, ok := GetPrivateKey()
	Assertf(ok, "private key not specified")
	return ret
}

func GetPrivateKey() (ed25519.PrivateKey, bool) {
	privateKeyStr := viper.GetString("wallet.private_key")
	if privateKeyStr == "" {
		return nil, false
	}
	ret, err := util.ED25519PrivateKeyFromHexString(privateKeyStr)
	return ret, err == nil
}

// without Var does not work
var targetStr string

func AddFlagTarget(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&targetStr, "target", "t", "", "target lock in EasyFL source format")
	err := viper.BindPFlag("target", cmd.PersistentFlags().Lookup("target"))
	AssertNoError(err)
}

func MustGetTarget() ledger.Accountable {
	var ret ledger.Accountable
	var err error

	//if str := viper.GetString("target"); str != "" {
	// viper.GetString does not work for some reason

	if targetStr != "" {
		ret, err = ledger.AccountableFromSource(targetStr)
		AssertNoError(err)
		Infof("target account is: %s", ret.String())
	} else {
		ret = GetWalletData().Account
		Infof("wallet account will be used as target: %s", ret.String())
	}
	return ret
}

var traceTx bool

func AddFlagTraceTx(cmd *cobra.Command) {
	cmd.PersistentFlags().BoolVarP(&traceTx, "trace", "x", false, "trace transaction on the node")
	err := viper.BindPFlag("trace", cmd.PersistentFlags().Lookup("trace"))
	AssertNoError(err)
}

func TraceTx() bool {
	return traceTx
}

func GetOwnSequencerID() *ledger.ChainID {
	seqIDStr := viper.GetString("wallet.sequencer_id")
	if seqIDStr == "" {
		return nil
	}
	ret, err := ledger.ChainIDFromHexString(seqIDStr)
	AssertNoError(err)
	return &ret
}

func BypassYesNoPrompt() bool {
	return viper.GetBool("force")
}

func ReadInConfig() {
	configName := viper.GetString("config")
	if configName == "" {
		configName = "proxi"
	}
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetConfigName(configName)
	viper.SetConfigFile("./" + configName + ".yaml")

	viper.AutomaticEnv() // read-in environment variables that match

	_ = viper.ReadInConfig()
	Infof("using profile: %s", viper.ConfigFileUsed())
}

func NoWait() bool {
	return viper.GetBool("nowait")
}

const waitSlotsBack = 2

func ReportTxInclusion(txid ledger.TransactionID, poll time.Duration) {
	weakFinality := getIsWeakFinality()

	Infof("Tracking inclusion of %s (hex=%s):", txid.String(), txid.StringHex())
	inclusionThresholdNumerator, inclusionThresholdDenominator := getInclusionThreshold()
	fin := "strong"
	if weakFinality {
		fin = "weak"
	}
	Infof("  finality criterion: %s, slot span: %d, strong inclusion threshold: %d/%d",
		fin, waitSlotsBack, inclusionThresholdNumerator, inclusionThresholdDenominator)
	for {
		score, err := GetClient().QueryTxInclusionScore(&txid, inclusionThresholdNumerator, inclusionThresholdDenominator, waitSlotsBack)
		AssertNoError(err)

		Infof("   weak score: %d%%, strong score: %d%%, from slot %d to %d (%d)",
			score.WeakScore, score.StrongScore, score.EarliestSlot, score.LatestSlot, score.LatestSlot-score.EarliestSlot+1)

		if weakFinality {
			if score.WeakScore == 100 {
				return
			}
		} else {
			if score.StrongScore == 100 {
				return
			}
		}
		time.Sleep(poll)
	}
}

func getInclusionThreshold() (int, int) {
	numerator := viper.GetInt("inclusion_threshold.numerator")
	denominator := viper.GetInt("inclusion_threshold.denominator")
	Assertf(multistate.ValidInclusionThresholdFraction(numerator, denominator), "wrong or missing inclusion threshold")
	return numerator, denominator
}

func getIsWeakFinality() bool {
	return viper.GetBool("weak")
}
