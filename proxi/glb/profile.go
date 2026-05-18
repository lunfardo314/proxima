package glb

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const LedgerDefinitionsFileName = "proxima.genesis.definitions.json"

type WalletData struct {
	PrivateKey ed25519.PrivateKey
	Account    ledger.SigLock
	Sequencer  *base.ChainID
}

func GetWalletData() (ret WalletData) {
	ret.PrivateKey = MustGetPrivateKey()
	ret.Account = ledger.SigLockFromED25519PrivateKey(ret.PrivateKey)
	ret.Sequencer = GetOwnSequencerID()

	// Consistency check: if wallet config has holder_id, it must match the key file
	configHolderID := viper.GetString("wallet.holder_id")
	if configHolderID != "" {
		derived := base.HolderIDFromPublicKey(base.SignatureTypeED25519, ret.PrivateKey.Public().(ed25519.PublicKey))
		derivedHex := hex.EncodeToString(derived[:])
		Assertf(configHolderID == derivedHex,
			"holder_id mismatch: wallet config has '%s', key file derives '%s'", configHolderID, derivedHex)
	}
	return
}

func MustGetPrivateKey() ed25519.PrivateKey {
	ret, ok := GetPrivateKey()
	Assertf(ok, "private key not specified")
	return ret
}

var cachedPrivateKey ed25519.PrivateKey

func GetPrivateKey() (ed25519.PrivateKey, bool) {
	if cachedPrivateKey != nil {
		return cachedPrivateKey, true
	}
	keyFile := viper.GetString("wallet.key_file")
	if keyFile == "" {
		return nil, false
	}
	ret, err := LoadPrivateKeyFromFile(keyFile)
	if err != nil {
		return nil, false
	}
	cachedPrivateKey = ret
	return ret, true
}

// without Var does not work
var targetStr string

func AddFlagTarget(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&targetStr, "target", "t", "", "target lock in EasyFL source format")
	err := viper.BindPFlag("target", cmd.PersistentFlags().Lookup("target"))
	AssertNoError(err)
}

// GetWalletAccount returns the wallet's SigLock derived from the public key in the keystore.
// Does NOT decrypt the private key, so no passphrase is needed.
func GetWalletAccount() ledger.SigLock {
	keyFile := viper.GetString("wallet.key_file")
	Assertf(keyFile != "", "wallet.key_file not configured")

	ks, err := keystore.LoadFromFile(keyFile)
	AssertNoError(err)

	pubKeyBytes, err := keystore.PublicKeyBytes(ks)
	AssertNoError(err)

	return ledger.SigLockFromED25519PublicKey(ed25519.PublicKey(pubKeyBytes))
}

func MustGetTarget() ledger.Controller {
	var ret ledger.Controller
	var err error

	if targetStr != "" {
		ret, err = ledger.ControllerFromSource(targetStr)
		AssertNoError(err)
		Infof("target account is: %s", ret.String())
	} else {
		ret = GetWalletAccount()
		Infof("wallet account (default as a target): %s ", ret.String())
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
	// Infof("default sequencer ID is: %s", seqIDStr)
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
	cfg := viper.ConfigFileUsed()
	Assertf(FileExists(cfg), "config profile '%s' not found", cfg)
	Infof("config profile: %s", cfg)

}

// TryReadInConfig attempts to load proxi.yaml but does not fail if it doesn't exist.
func TryReadInConfig() {
	configName := viper.GetString("config")
	if configName == "" {
		configName = "proxi"
	}
	viper.AddConfigPath(".")
	viper.SetConfigType("yaml")
	viper.SetConfigName(configName)
	viper.SetConfigFile("./" + configName + ".yaml")

	viper.AutomaticEnv()

	if err := viper.ReadInConfig(); err == nil {
		Infof("config profile: %s", viper.ConfigFileUsed())
	}
}

func NoWait() bool {
	return viper.GetBool("nowait")
}

func TrackTxInclusion(txid base.TransactionID, poll time.Duration, timeout ...time.Duration) bool {
	defer PrintTxLogForTxID(txid)

	inclusionDepth := GetTargetInclusionDepth()
	Infof("tracking inclusion of %s, target depth: %d", txid.StringShort(), inclusionDepth)
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
				fmt.Printf("\r\033[K%2d sec. LRB: %s  transaction NOT included", since, lrbidStr)
			} else {
				fmt.Printf("\r\033[K%2d sec. LRB: %s  included at depth %d", since, lrbidStr, foundAtDepth)
				if foundAtDepth == inclusionDepth {
					fmt.Println()
					Infof("target inclusion depth %d has been reached", inclusionDepth)
					return true
				}
			}
			last = time.Now()
			lrbids.Insert(lrbid)
		}
		time.Sleep(poll)
		if len(timeout) > 0 && time.Since(start) > timeout[0] {
			fmt.Println()
			return false
		}
	}
}

const maxLogLines = 200

func PrintTxLogForTxID(txid base.TransactionID) {
	prefix := txid.ShortID()
	resp, err := GetClient().TxLogGet(hex.EncodeToString(prefix[:]), maxLogLines)
	if err != nil {
		Infof("transaction log not available: %v", err)
		return
	}
	Infof("\n---- txlog of %s (%d records) ----\n ", txid.String(), len(resp.Records))
	SortAndPrintTxLog(resp.Records)
}

func SortAndPrintTxLog(recs []api.TxLogRecord) {
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].ClockTimestamp < recs[j].ClockTimestamp
	})

	const txidFieldWidth = 30

	for _, rec := range recs {
		ts := time.Unix(0, rec.ClockTimestamp).UTC()
		txid, err := base.TransactionIDFromHexString(rec.TxID)
		AssertNoError(err)

		Infof("  %s %-*s %s", ts.Format("15:04:05.000"), txidFieldWidth, txid.StringShort(), rec.Message)
	}
}

func GetTagAlongFee() uint64 {
	return viper.GetUint64("tag_along.fee")
}

var tagAlongSequencerID atomic.Pointer[base.ChainID]

func GetTagAlongSequencerID(doNotCallNode ...bool) *base.ChainID {
	ret := tagAlongSequencerID.Load()
	if ret != nil {
		return ret
	}

	seqIDStr := viper.GetString("tag_along.sequencer_id")
	var seqID base.ChainID
	var err error
	if seqIDStr == "" {
		// Infof("tag-along sequencer is not configured. Trying default..")
		pseqID := GetDefaultSequencerID()
		Assertf(pseqID != nil, "default sequencer not specified")
		seqID = *pseqID
	} else {
		seqID, err = base.ChainIDFromHexString(seqIDStr)
		AssertNoError(err)
	}

	if len(doNotCallNode) > 0 && !doNotCallNode[0] {
		o, _, err := GetClient().GetChainOutputData(seqID)
		Assertf(err == nil, "can't find chain %s: %v", seqID.String(), err)
		Assertf(o.ID.IsSequencerTransaction(), "can't get tag-along sequencer %s: chain output %s is not a sequencer output",
			seqID.StringShort(), o.ID.StringShort())
	}

	tagAlongSequencerID.Store(&seqID)
	return &seqID
}

func GetTargetInclusionDepth() int {
	if TargetInclusionDepth < 0 {
		return 1
	}
	return TargetInclusionDepth
}
