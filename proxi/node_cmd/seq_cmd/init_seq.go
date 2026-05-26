package seq_cmd

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/keystore"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func initSeqInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init <amount> [<flags>]",
		Short: `creates a new sequencer chain origin and updates proxi.yaml / proxima.yaml`,
		Long: `Creates a fresh sequencer chain whose origin output holds <amount> tokens via
a sequencer transaction endorsing the tag-along sequencer's latest milestone.

Optional flags fall into two groups:

  sequencer-data flags (mutable; seed the chain's slot-5 milestone data —
  same set as 'proxi node seq set-params'):
    --name, --fee, --margin, --greedy, --pace, --ignore-freeze-bound

  delegation-params flags (immutable; embedded in the chain's sequencer
  constraint at slot 4):
    --epoch-slots, --max-frozen-epochs

Any absent flag uses its library default. Absent --name produces a nameless
sequencer.`,
		Args: cobra.ExactArgs(1),
		Run:  runSeqInitCmd,
	}

	c.Flags().String("name", "", "sequencer name (1-6 chars; absent => nameless)")
	c.Flags().Uint64("fee", 0, "minimum tag-along fee")
	c.Flags().Uint16("margin", 0, "inflation profit margin promille (0-1000)")
	c.Flags().Bool("greedy", false, "greedy flag")
	c.Flags().Uint8("pace", 0, "pace value (ticks)")
	c.Flags().Bool("ignore-freeze-bound", false, "ignore upper bound on freeze")
	c.Flags().Uint32("epoch-slots", 0, "delegation epoch slots (default: library default)")
	c.Flags().Uint8("max-frozen-epochs", 0, "max frozen epochs (default: library default)")

	c.InitDefaultHelpCmd()
	return c
}

func runSeqInitCmd(cmd *cobra.Command, args []string) {
	walletData := glb.GetWalletData()
	glb.Infof("wallet account is: %s", walletData.Account.String())

	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)
	glb.Infof("amount: %s", util.Th(amount))

	consts := glb.GetLedgerConstants()

	// Delegation params (immutable; embedded in the sequencer constraint).
	// Absent flags fall back to the library defaults provided by the node.
	epochSlots := consts.DelegationEpochSlots
	maxFrozenEpochs := byte(consts.MaxFrozenEpochs)
	if cmd.Flags().Changed("epoch-slots") {
		v, _ := cmd.Flags().GetUint32("epoch-slots")
		epochSlots = v
	}
	if cmd.Flags().Changed("max-frozen-epochs") {
		v, _ := cmd.Flags().GetUint8("max-frozen-epochs")
		maxFrozenEpochs = v
	}

	// Optional initial sequencer data — same flag set as set-params. Pushed as
	// an inline-data constraint at slot 5 of the chain origin output iff at
	// least one of the flags was supplied. Absent flags => no slot-5 entry
	// (omitempty JSON => empty SequencerData).
	sd := seqdata.SequencerData{}
	sdProvided := false
	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		glb.Assertf(len(v) <= 6, "name must be empty or 1-6 characters")
		sd.SetName(v)
		sdProvided = true
	}
	if cmd.Flags().Changed("fee") {
		v, _ := cmd.Flags().GetUint64("fee")
		sd.SetMinimumFee(v)
		sdProvided = true
	}
	if cmd.Flags().Changed("margin") {
		v, _ := cmd.Flags().GetUint16("margin")
		glb.Assertf(v <= 1000, "margin must be 0-1000")
		sd.SetSeqProfitMarginPromille(v)
		sdProvided = true
	}
	if cmd.Flags().Changed("greedy") {
		v, _ := cmd.Flags().GetBool("greedy")
		sd.SetGreedy(v)
		sdProvided = true
	}
	if cmd.Flags().Changed("pace") {
		v, _ := cmd.Flags().GetUint8("pace")
		sd.SetPace(v)
		sdProvided = true
	}
	if cmd.Flags().Changed("ignore-freeze-bound") {
		v, _ := cmd.Flags().GetBool("ignore-freeze-bound")
		sd.SetIgnoreFreezeBound(v)
		sdProvided = true
	}

	waitForFunds(glb.MustGetTarget(), amount)

	var initialSeqData *seqdata.SequencerData
	if sdProvided {
		initialSeqData = &sd
	}
	cid, txid, err := makeSequencerChainOrigin(amount, epochSlots, maxFrozenEpochs, initialSeqData)
	glb.AssertNoError(err)
	glb.Infof("new sequencer chain id is %s", cid.String())
	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}

	updateWalletConfig(cid)
	updateNodeConfig(sd.Name(), walletData.PrivateKey, cid)
}

// makeSequencerChainOrigin composes and submits a sequencer transaction whose
// first produced output is the new sequencer chain origin. The sequencer
// constraint at slot 4 carries (epochSlots, maxFrozenEpochs); if
// initialSeqData is non-nil, its encoded JSON is pushed at slot 5 as inline
// milestone data. The tx endorses the tag-along sequencer's latest milestone
// (the cheapest sequencer-tx endorsement available to the wallet, required by
// _noChainPredecessorCase in def/sequencer.easyfl).
func makeSequencerChainOrigin(
	onChainAmount uint64,
	epochSlots uint32,
	maxFrozenEpochs byte,
	initialSeqData *seqdata.SequencerData,
) (chainID base.ChainID, txid base.TransactionID, err error) {
	walletData := glb.GetWalletData()
	target := glb.MustGetTarget()
	clnt := glb.GetClient()
	consts := glb.GetLedgerConstants()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)
	feeAmount := glb.GetTagAlongFee()
	if seqMinFee > feeAmount {
		feeAmount = seqMinFee
	}

	// Endorsement target: latest milestone of the tag-along sequencer
	// (the only sequencer chain the wallet is guaranteed to know about).
	endorseTxid, err := latestSequencerMilestone(clnt, *tagAlongSeqID)
	if err != nil {
		return base.NilChainID, base.TransactionID{}, err
	}

	glb.Infof("Creating new sequencer chain origin:")
	glb.Infof("   on-chain balance: %s", util.Th(onChainAmount))
	glb.Infof("   tag-along fee %s to the sequencer %s", util.Th(feeAmount), tagAlongSeqID.String())
	glb.Infof("   source account: %s", walletData.Account.String())
	glb.Infof("   total cost: %s", util.Th(onChainAmount+feeAmount))
	glb.Infof("   chain controller: %s", target)
	glb.Infof("   sequencer params: epochSlots=%d, maxFrozenEpochs=%d", epochSlots, maxFrozenEpochs)
	if initialSeqData != nil {
		glb.Infof("   initial sequencer data: %s", string(initialSeqData.Bytes()))
	} else {
		glb.Infof("   initial sequencer data: (none)")
	}
	glb.Infof("   endorsing tag-along sequencer milestone: %s", endorseTxid.StringShort())

	if !glb.YesNoPrompt("proceed?:", true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	inps, lrbid, totalInputs, err := clnt.GetTransferableOutputs(walletData.Account)
	glb.AssertNoError(err)
	if onChainAmount == 0 {
		onChainAmount = totalInputs - feeAmount
	}
	glb.Assertf(totalInputs >= onChainAmount+feeAmount, "not enough source balance %s", util.Th(totalInputs))
	glb.PrintLRB(lrbid)

	picked := uint64(0)
	inps = util.PurgeSlice(inps, func(o *ledger.OutputWithID) bool {
		if picked < onChainAmount+feeAmount {
			picked += o.Output.TokenBalance()
			return true
		}
		return false
	})

	// Wallet-derived "now" — pace-enforced against the latest input and
	// the endorsed milestone (same-slot endorsements require the
	// endorsement to be strictly older than the tx).
	ts := consts.LedgerTimeFromClockTime(time.Now())
	for _, in := range inps {
		ts = base.MaximumTime(ts, in.Timestamp().AddTicks(int(consts.TransactionPace)))
	}
	ts = base.MaximumTime(ts, endorseTxid.Timestamp().AddTicks(int(consts.TransactionPaceSequencer)))
	if ts.IsSlotBoundary() {
		// Sequencer origin cannot be a branch (tick==0) — see
		// _noChainPredecessorCase.
		ts = ts.AddTicks(1)
	}

	txBytes, txid, chainOutIdx, consumed, err := composeSequencerChainOriginTx(
		walletData.PrivateKey, inps, target, onChainAmount,
		*tagAlongSeqID, feeAmount, ts, endorseTxid,
		epochSlots, maxFrozenEpochs, initialSeqData,
	)
	if err != nil {
		return base.NilChainID, base.TransactionID{}, err
	}
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		return base.NilChainID, base.TransactionID{}, err
	}

	chainOid := base.MustNewOutputID(txid, chainOutIdx)
	chainID = base.MakeOriginChainID(chainOid)
	return chainID, txid, nil
}

// composeSequencerChainOriginTx is the pure wasm-wallet compose helper
// for the sequencer-chain-origin tx. It mirrors makeChainOriginTransaction
// but additionally:
//   - attaches the 2-arg sequencer constraint at slot 4 of the chain
//     origin output;
//   - if initialSeqData is non-nil, pushes its JSON bytes as an inline-data
//     constraint at slot 5 (sequencer milestone data);
//   - calls SetSequencerData(chainOutIdx, SequencerOutputIndexNone) so
//     the tx is marked as a sequencer transaction (selfOutputIndex ==
//     txSequencerOutputIndex check passes);
//   - endorses one sequencer tx (required by _noChainPredecessorCase).
func composeSequencerChainOriginTx(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	target ledger.Lock,
	onChainAmount uint64,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	ts base.LedgerTime,
	endorseTxid base.TransactionID,
	epochSlots uint32,
	maxFrozenEpochs byte,
	initialSeqData *seqdata.SequencerData,
) (txBytes []byte, txid base.TransactionID, chainOutIdx byte, consumed [][]byte, err error) {
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	txb := txbuildercore.New(0)

	consumedTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		consumedTotal += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, 0, nil, err
			}
		}
	}

	// Chain-origin output: target lock + chainOrigin at slot 3 + sequencer
	// constraint at slot 4 + optional sequencer milestone data at slot 5.
	baseChainOut, err := glb.BuildLockOutput(lib, onChainAmount, target)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder, err := txbuildercore.OutputBuilderFromBytes(baseChainOut.Bytes())
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBin, err := lib.NewChainOrigin(ts.Slot)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder.MustPushConstraint(chainOriginBin)
	seqBin, err := lib.NewSequencerConstraintBytecode(epochSlots, maxFrozenEpochs)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	chainOriginBuilder.MustPushConstraint(seqBin)
	if initialSeqData != nil {
		chainOriginBuilder.MustPushConstraint(engine.InlineDataBytecode(initialSeqData.Bytes()))
	}
	chainOutIdx = txb.ProduceOutput(chainOriginBuilder.Output().Bytes())

	if tagAlongFee > 0 {
		tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(tagAlongOut.Bytes())
	}
	if consumedTotal > onChainAmount+tagAlongFee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, consumedTotal-onChainAmount-tagAlongFee, walletHolderID)
		if err != nil {
			return nil, base.TransactionID{}, 0, nil, err
		}
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	// Mark as sequencer tx so the sequencer constraint's check
	// `selfOutputIndex == txSequencerOutputIndex` passes for the chain
	// origin output. Non-branch → stem index is SequencerOutputIndexNone.
	txb.SetSequencerData(chainOutIdx, txbuildercore.SequencerOutputIndexNone)
	txb.TxData.Endorsements = append(txb.TxData.Endorsements, endorseTxid)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	return txBytes, txid, chainOutIdx, consumed, nil
}

// latestSequencerMilestone fetches the tag-along sequencer's latest
// milestone TxID via /last_known_milestones. The wallet asserts the
// chosen sequencer is alive in the host's tippool — if it isn't, the
// host has no candidate for us to endorse and the call must fail.
func latestSequencerMilestone(clnt *client.APIClient, seqID base.ChainID) (base.TransactionID, error) {
	tips, err := clnt.GetLastKnownSequencerData()
	if err != nil {
		return base.TransactionID{}, err
	}
	tip, ok := tips[seqID.StringHex()]
	if !ok || tip.LatestMilestoneTxID == "" {
		return base.TransactionID{}, fmt.Errorf("tag-along sequencer %s has no known milestone — cannot endorse",
			seqID.StringShort())
	}
	return base.TransactionIDFromHexString(tip.LatestMilestoneTxID)
}

func waitForFunds(accountable ledger.Controller, amount uint64) {
	for {
		res, err := glb.GetClient().GetOutputsForControllerID(accountable.ControllerID(), client.GetOutputsParams{
			LockType:  api.GetOutputsLockTypeSigLock,
			Chained:   client.NonChainedOnly(),
			ForAmount: amount,
		})
		glb.AssertNoError(err)
		if res.AvailableAmount >= amount {
			break
		}
		time.Sleep(1 * time.Second)
	}
}

func updateWalletConfig(chainId base.ChainID) {
	data, err := os.ReadFile("proxi.yaml")
	glb.AssertNoError(err)

	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	if wallet, ok := config["wallet"].(map[interface{}]interface{}); ok {
		wallet["sequencer_id"] = chainId.StringHex()
	}

	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)
	err = os.WriteFile("proxi.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}

func updateNodeConfig(name string, key ed25519.PrivateKey, chainId base.ChainID) {
	publicKey := key.Public().(ed25519.PublicKey)
	sid := base.HolderIDFromPublicKey(base.SignatureTypeED25519, publicKey)
	holderID := hex.EncodeToString(sid[:])
	seqKeyFile := keystore.DefaultKeyFile

	ks, err := keystore.NewUnencrypted(keystore.KeyTypeED25519, key, publicKey, holderID)
	glb.AssertNoError(err)

	if glb.YesNoPrompt("Encrypt the sequencer key file with a passphrase?", false) {
		passphrase := glb.ReadPassphraseConfirm()
		ks, err = keystore.EncryptKeystore(ks, passphrase, "")
		glb.AssertNoError(err)
		glb.Infof("Key encrypted.")
	}

	err = ks.SaveToFile(seqKeyFile)
	glb.AssertNoError(err)
	glb.Infof("sequencer controller key saved to '%s'", seqKeyFile)

	data, err := os.ReadFile("proxima.yaml")
	glb.AssertNoError(err)

	var config map[string]interface{}
	err = yaml.Unmarshal(data, &config)
	glb.AssertNoError(err)

	if sequencer, ok := config["sequencer"].(map[interface{}]interface{}); ok {
		sequencer["name"] = name
		sequencer["enable"] = true
		sequencer["chain_id"] = chainId.StringHex()
		sequencer["controller_key_file"] = seqKeyFile
		delete(sequencer, "controller_key")
	} else {
		glb.Infof("!!! Error sequencer key not found")
	}

	modifiedData, err := yaml.Marshal(&config)
	glb.AssertNoError(err)
	err = os.WriteFile("proxima.yaml", modifiedData, 0600)
	glb.AssertNoError(err)
}
