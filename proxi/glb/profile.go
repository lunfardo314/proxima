package glb

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/proxima/core/inclusion"
	"github.com/lunfardo314/proxima/core/vertex"
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

func ReportTxStatus(txid ledger.TransactionID, poll time.Duration, strongFinalityV ...bool) {
	strongFinality := len(strongFinalityV) > 0 && strongFinalityV[0]

	Infof("Transaction %s (hex=%s):", txid.String(), txid.StringHex())
	inclusionThresholdNumerator, inclusionThresholdDenominator := getInclusionThreshold()
	Infof("Inclusion threshold: %d/%d :", inclusionThresholdNumerator, inclusionThresholdDenominator)
	latestSlot := ledger.Slot(0)
	numSlots := 0
	for {
		vertexStatus, inclusionData, err := GetClient().QueryTxIDStatus(&txid)
		AssertNoError(err)

		Verbosef(vertexStatus.Lines().Join(", "))
		Verbosef("%s", inclusion.Lines(inclusionData, inclusionThresholdNumerator, inclusionThresholdDenominator, "    "))

		if len(inclusionData) == 0 {
			time.Sleep(poll)
			continue
		}
		slot, strongScore, weakScore := inclusion.Score(inclusionData, inclusionThresholdNumerator, inclusionThresholdDenominator)
		Infof("   slot: %d, strongScore: %d%%, weakScore: %d%%", latestSlot, strongScore, weakScore)
		if latestSlot != slot {
			numSlots++
		}
		switch {
		case vertexStatus.Deleted || vertexStatus.Status == vertex.Bad:
			return
		case numSlots < 2:
			continue
		case strongFinality:
			if strongScore == 100 {
				return
			}
		default:
			if weakScore == 100 {
				return
			}
		}
		time.Sleep(poll)
	}
}

func getInclusionThreshold() (uint64, uint64) {
	numerator := uint64(viper.GetInt("inclusion_threshold.numerator"))
	denominator := uint64(viper.GetInt("inclusion_threshold.denominator"))

	Assertf(multistate.ValidInclusionThresholdFraction(numerator, denominator), "wrong or missing inclusion threshold")
	return numerator, denominator
}
