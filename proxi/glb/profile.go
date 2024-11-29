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

const LedgerIDFileName = "proxima.genesis.id.yaml"

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

func GetOwnSequencerID() *ledger.ChainID {
	seqIDStr := viper.GetString("wallet.sequencer_id")
	if seqIDStr == "" {
		return nil
	}
	ret, err := ledger.ChainIDFromHexString(seqIDStr)
	if err != nil {
		return nil
	}
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

const slotSpan = 2

func ReportTxInclusion(txid ledger.TransactionID, poll time.Duration, maxSlots ...ledger.Slot) {
	weakFinality := GetIsWeakFinality()

	if len(maxSlots) > 0 {
		Infof("Tracking inclusion of %s (hex=%s) for at most %d slots:", txid.String(), txid.StringHex(), maxSlots[0])
	} else {
		Infof("Tracking inclusion of %s (hex=%s):", txid.String(), txid.StringHex())
	}
	inclusionThresholdNumerator, inclusionThresholdDenominator := GetInclusionThreshold()
	fin := "strong"
	if weakFinality {
		fin = "weak"
	}
	Infof("  finality criterion: %s, slot span: %d, strong inclusion threshold: %d/%d",
		fin, slotSpan, inclusionThresholdNumerator, inclusionThresholdDenominator)

	startSlot := ledger.TimeNow().Slot()
	for {
		score, err := GetClient().QueryTxInclusionScore(txid, inclusionThresholdNumerator, inclusionThresholdDenominator, slotSpan)
		AssertNoError(err)

		lrbid, err := ledger.TransactionIDFromHexString(score.LRBID)
		AssertNoError(err)

		slotsBack := ledger.TimeNow().Slot() - lrbid.Slot()
		Infof("   weak score: %d%%, strong score: %d%%, slot span %d - %d (%d), included in LRB: %v, LRB is slots back: %d",
			score.WeakScore, score.StrongScore, score.EarliestSlot, score.LatestSlot, score.LatestSlot-score.EarliestSlot+1,
			score.IncludedInLRB, slotsBack)

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

		slotNow := ledger.TimeNow().Slot()
		if len(maxSlots) > 0 && maxSlots[0] < slotNow-startSlot {
			Infof("----- failed to reach finality in %d slots", maxSlots[0])
			return
		}
	}
}

func GetInclusionThreshold() (int, int) {
	numerator := viper.GetInt("finality.inclusion_threshold.numerator")
	denominator := viper.GetInt("finality.inclusion_threshold.denominator")
	Assertf(multistate.ValidInclusionThresholdFraction(numerator, denominator), "wrong or missing inclusion threshold")
	return numerator, denominator
}

func GetIsWeakFinality() bool {
	return viper.GetBool("finality.weak")
}
