package glb

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
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
		// no wallet key configured at all — the only "not specified" case
		return nil, false
	}
	ret, err := LoadPrivateKeyFromFile(keyFile)
	// A configured key file that cannot be loaded (wrong passphrase, corrupted
	// keystore) is an error, not a missing key: report it instead of letting the
	// caller report the misleading "private key not specified".
	AssertNoError(err)
	cachedPrivateKey = ret
	return ret, true
}

// without Var does not work
var targetStr string

func AddFlagTarget(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVarP(&targetStr, "target", "t", "", "target siglock (a/..) or chain lock (c/..)")
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
				fmt.Printf("\n%2d sec. LRB: %s  transaction NOT included", since, lrbidStr)
			} else {
				fmt.Printf("\n%2d sec. LRB: %s  included at depth %d", since, lrbidStr, foundAtDepth)
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

// TagAlongSequencerRandom is the tag_along.sequencer_id value that asks the
// wallet to pick a currently active sequencer instead of naming one. It is a
// complete specification of the target, so it never falls back to the default
// sequencer: an operator who asks for a live target is better served by an
// error than by a silent tag-along to a sequencer that may be long gone.
const TagAlongSequencerRandom = "random"

// TagAlongSequencerIsRandom reports whether the profile leaves the target to be
// picked from live activity. Lets display paths say so instead of resolving,
// which would need a node and would pick a target nothing is going to use.
func TagAlongSequencerIsRandom() bool {
	return viper.GetString("tag_along.sequencer_id") == TagAlongSequencerRandom
}

// activeSequencerSlots is how far back a sequencer's latest milestone may lie
// and still count as active. One slot: a live sequencer issues several
// milestones per slot, so anything quiet for a whole slot is not one to hand a
// transaction to.
const activeSequencerSlots = 1

// GetTagAlongSequencerID resolves the tag-along sequencer from the wallet
// profile: an explicit ID, 'random' to pick among the currently active ones, or
// the default sequencer when unset. By default it verifies against the node that
// the ID exists on the ledger and is a sequencer, failing with a clear error
// otherwise — a stale or wrong tag_along.sequencer_id would otherwise be
// accepted silently and every resulting transaction would tag-along to a phantom
// sequencer and never confirm. Pass doNotCallNode=true to skip the node check
// (offline / display); 'random' cannot be resolved that way since it is decided
// from live data.
//
// The result is resolved once per process. A command that asks twice — say to
// price the fee and then to build the output — must not be handed two different
// sequencers.
func GetTagAlongSequencerID(doNotCallNode ...bool) *base.ChainID {
	ret := tagAlongSequencerID.Load()
	if ret != nil {
		return ret
	}
	offline := len(doNotCallNode) > 0 && doNotCallNode[0]

	seqIDStr := viper.GetString("tag_along.sequencer_id")
	var seqID base.ChainID
	var err error
	switch seqIDStr {
	case TagAlongSequencerRandom:
		Assertf(!offline, "tag_along.sequencer_id is '%s', which can only be resolved against a node", TagAlongSequencerRandom)
		seqID = randomActiveSequencerID()
		Infof("tag-along sequencer picked at random among the active ones: %s", seqID.String())
		// no ledger check below: it was just picked from live sequencer activity,
		// which is stronger evidence than presence in the state
		tagAlongSequencerID.Store(&seqID)
		return &seqID
	case "":
		pseqID := GetDefaultSequencerID()
		Assertf(pseqID != nil, "default sequencer not specified")
		seqID = *pseqID
	default:
		seqID, err = base.ChainIDFromHexString(seqIDStr)
		AssertNoError(err)
	}

	if !offline {
		o, _, err := GetClient().GetChainOutputData(seqID)
		if errors.Is(err, multistate.ErrNotFound) {
			Fatalf("tag-along sequencer %s not found on the ledger — check tag_along.sequencer_id in the wallet profile (leave it empty to use the default sequencer, or set it to '%s')", seqID.String(), TagAlongSequencerRandom)
		}
		Assertf(err == nil, "cannot resolve tag-along sequencer %s: %v", seqID.String(), err)
		Assertf(o.ID.IsSequencerTransaction(), "tag-along %s is not a sequencer output (chain output %s)",
			seqID.StringShort(), o.ID.StringShort())
	}

	tagAlongSequencerID.Store(&seqID)
	return &seqID
}

// randomActiveSequencerID picks uniformly among the sequencers whose latest
// known milestone is no older than activeSequencerSlots. Activity is judged in
// ledger time rather than by the node's wall-clock 'last activity' stamp, so the
// answer does not depend on how long the transaction sat in the node's tippool.
func randomActiveSequencerID() base.ChainID {
	known, err := GetClient().GetLastKnownSequencerData()
	AssertNoError(err)

	nowSlot := GetLedgerTimeNow().Slot
	active := make([]base.ChainID, 0, len(known))
	for seqIDStr, d := range known {
		// malformed entries are not skipped: quietly dropping one would narrow
		// the draw, or report nobody active, with no sign of why
		seqID, err := base.ChainIDFromHexString(seqIDStr)
		Assertf(err == nil, "cannot parse sequencer ID '%s' reported by the node: %v", seqIDStr, err)
		txid, err := base.TransactionIDFromHexString(d.LatestMilestoneTxID)
		Assertf(err == nil, "cannot parse latest milestone '%s' of sequencer %s reported by the node: %v",
			d.LatestMilestoneTxID, seqID.StringShort(), err)

		if slot := txid.Slot(); slot+activeSequencerSlots >= nowSlot {
			active = append(active, seqID)
			Verbosef("active sequencer %s, latest milestone in slot %d (now %d)", seqID.StringShort(), slot, nowSlot)
		}
	}
	Assertf(len(active) > 0, "no sequencer has been active in the last %d slot(s): cannot pick a tag-along target at random",
		activeSequencerSlots)

	return active[rand.IntN(len(active))]
}

func GetTargetInclusionDepth() int {
	if TargetInclusionDepth < 0 {
		return 1
	}
	return TargetInclusionDepth
}
