package glb

import (
	"crypto/ed25519"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const LedgerIDFileName = "proxima.genesis.id.yaml"

type WalletData struct {
	PrivateKey ed25519.PrivateKey
	Account    ledger.AddressED25519
	Sequencer  *base.ChainID
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

func GetDefaultSequencerID() *base.ChainID {
	seqIDStr := viper.GetString("default_sequencer_id")
	if seqIDStr == "" {
		return nil
	}
	ret, err := base.ChainIDFromHexString(seqIDStr)
	if err != nil {
		Infof("invalid default sequencer ID: %v", err)
		return nil
	}
	Infof("default sequencer ID is: %s", seqIDStr)
	return &ret

}

func GetOwnSequencerID() *base.ChainID {
	seqIDStr := viper.GetString("wallet.sequencer_id")
	if seqIDStr == "" {
		Infof("own sequencer ID not specified. Using default sequencer ID instead")
		return GetDefaultSequencerID()
	}
	ret, err := base.ChainIDFromHexString(seqIDStr)
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

func TrackTxInclusion(txid base.TransactionID, poll time.Duration) {
	inclusionDepth := GetTargetInclusionDepth()
	Infof("tracking inclusion of the transaction %s.\ntarget inclusion depth: %d", txid.String(), inclusionDepth)
	lrbids := set.New[base.TransactionID]()
	clnt := GetClient()
	start := time.Now()
	last := time.Now()
	for {
		lrbid, foundAtDepth, err := clnt.CheckTransactionIDInLRB(txid, inclusionDepth)
		AssertNoError(err)

		if time.Since(last) > poll*4 || !lrbids.Contains(lrbid) {
			lrbidStr := lrbid.StringShort()
			if IsVerbose() {
				lrbidStr += ", hex=" + lrbid.StringHex()
			}
			since := time.Since(start) / time.Second
			if foundAtDepth < 0 {
				Infof("%2d sec. Transaction is NOT included in the latest reliable branch (LRB) %s", since, lrbidStr)
			} else {
				Infof("%2d sec. Transaction is INCLUDED in the latest reliable branch (LRB) at depth %d: %s", since, foundAtDepth, lrbidStr)
				if foundAtDepth == inclusionDepth {
					Infof("target inclusion depth %d has been reached", inclusionDepth)
					return
				}
			}
			last = time.Now()
			lrbids.Insert(lrbid)
		}
		time.Sleep(poll)
	}
}

func GetTagAlongFee() uint64 {
	return viper.GetUint64("tag_along.fee")
}

var tagAlongSequencerID atomic.Pointer[base.ChainID]

func GetTagAlongSequencerID() *base.ChainID {
	ret := tagAlongSequencerID.Load()
	if ret != nil {
		return ret
	}

	seqIDStr := viper.GetString("tag_along.sequencer_id")
	var seqID base.ChainID
	var err error
	if seqIDStr == "" {
		Infof("tag-along sequencer is not configured. Trying default..")
		pseqID := GetDefaultSequencerID()
		Assertf(pseqID != nil, "default sequencer not specified")
		seqID = *pseqID
	} else {
		seqID, err = base.ChainIDFromHexString(seqIDStr)
		AssertNoError(err)
	}

	o, _, err := GetClient().GetChainOutputData(seqID)
	Assertf(err == nil, "can't find chain %s: %v", seqID.String(), err)
	Assertf(o.ID.IsSequencerTransaction(), "can't get tag-along sequencer %s: chain output %s is not a sequencer output",
		seqID.StringShort(), o.ID.StringShort())

	tagAlongSequencerID.Store(&seqID)
	return &seqID
}

func GetTargetInclusionDepth() int {
	if TargetInclusionDepth < 0 {
		return 1
	}
	return TargetInclusionDepth
}
