package glb

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/proxima/core/inclusion"
	"github.com/lunfardo314/proxima/core/vertex"
	"github.com/lunfardo314/proxima/core/work_process/tippool"
	"github.com/lunfardo314/proxima/ledger"
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

func ReportTxStatus(txid ledger.TransactionID, poll time.Duration, stopFun ...func(status *vertex.TxIDStatus, inclusionData map[ledger.ChainID]tippool.TxInclusion) bool) {
	stop := func(_ *vertex.TxIDStatus, inclusionData map[ledger.ChainID]tippool.TxInclusion) bool {
		percTotal, _ := inclusion.ScoreLatestSlot(inclusionData)
		return percTotal == 100
	}
	if len(stopFun) > 0 {
		stop = stopFun[0]
	}

	Infof("Transaction %s (hex=%s):", txid.String(), txid.StringHex())
	for {
		vertexStatus, inclusionData, err := GetClient().QueryTxIDStatus(&txid)
		AssertNoError(err)
		Infof(vertexStatus.Lines().Join(", "))
		if len(inclusionData) > 0 {
			_, incl := inclusion.InLatestSlot(inclusionData)
			percTotal, percDominating := incl.Score()
			Infof("   total branches: %d, included in total: %d%%, in dominating: %d%%", incl.NumBranchesTotal, percTotal, percDominating)
		}
		if vertexStatus.Deleted || vertexStatus.Status == vertex.Bad {
			return
		}
		if stop(vertexStatus, inclusionData) {
			return
		}
		time.Sleep(poll)
	}
}
