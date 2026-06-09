package seq_cmd

import (
	"crypto/ed25519"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/easyfl/engine"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func initSeqInitCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "init_genesis <amount> [<flags>]",
		Short: `creates a new sequencer chain origin output (wallet-side); does NOT touch proxima.yaml`,
		Long: `Creates a fresh sequencer chain whose origin output holds <amount> tokens.
The producing transaction is a regular wallet tx (no ` + "`s`" + ` bit, no endorsements);
the new chain reaches the tangle via its tag-along output.

This command is the WALLET-SIDE concern only:
  - submits the chain-origin transaction;
  - records the new chain ID in proxi.yaml (wallet.sequencer_id).

The NODE-SIDE configuration (proxima.yaml's sequencer section + controller
key file) is the operator's manual job — edit proxima.yaml directly, and
copy/symlink/encrypt the controller key file as you see fit.

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

	// Resolve the target once up front: MustGetTarget logs the resolved address,
	// and the prior code called it twice (here + inside makeSequencerChainOrigin),
	// producing two identical "wallet account (default as a target): …" lines.
	target := glb.MustGetTarget()

	var initialSeqData *seqdata.SequencerData
	if sdProvided {
		initialSeqData = &sd
	}
	cid, txid := makeSequencerChainOrigin(target, amount, epochSlots, maxFrozenEpochs, initialSeqData)
	glb.Infof("new sequencer chain id is %s", cid.String())
	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}

	updateWalletConfig(cid)
	glb.Infof("\nproxi.yaml updated: wallet.sequencer_id = %s", cid.StringHex())
	glb.Infof("Next step (manual): edit proxima.yaml's `sequencer` section on the node — set")
	glb.Infof("  chain_id, controller_key_file, enable: true, and any operational flags you want.")
}

// makeSequencerChainOrigin composes and submits a sequencer transaction whose
// first produced output is the new sequencer chain origin. The sequencer
// constraint at slot 4 carries (epochSlots, maxFrozenEpochs); if initialSeqData
// is non-nil, its encoded JSON is pushed at slot 5 as inline milestone data.
//
// The tx has NO endorsements — _noChainPredecessorCase in def/sequencer.easyfl
// explicitly forbids them. The chain origin is naturally pulled into the tangle
// via its tag-along output (consumed by the tag-along sequencer).
//
// On submit failure SubmitAndDisplay has already printed the error and the
// failing-tx pretty-form; this helper exits(1) directly so the CLI doesn't
// print the error a second time.
func makeSequencerChainOrigin(
	target ledger.Lock,
	onChainAmount uint64,
	epochSlots uint32,
	maxFrozenEpochs byte,
	initialSeqData *seqdata.SequencerData,
) (chainID base.ChainID, txid base.TransactionID) {
	walletData := glb.GetWalletData()
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

	// Wallet-derived "now" — pace-enforced against the latest input.
	ts := glb.GetLedgerTimeNow()
	for _, in := range inps {
		ts = base.MaximumTime(ts, in.Timestamp().AddTicks(int(consts.TransactionPace)))
	}
	if ts.IsSlotBoundary() {
		// Sequencer origin cannot be a branch (tick==0) — see
		// _noChainPredecessorCase.
		ts = ts.AddTicks(1)
	}

	txBytes, txid, chainOutIdx, consumed, err := composeSequencerChainOriginTx(
		walletData.PrivateKey, inps, target, onChainAmount,
		*tagAlongSeqID, feeAmount, ts,
		epochSlots, maxFrozenEpochs, initialSeqData,
	)
	glb.AssertNoError(err)
	if err = glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		// SubmitAndDisplay already printed the failure detail.
		os.Exit(1)
	}

	chainOid := base.MustNewOutputID(txid, chainOutIdx)
	chainID = base.MakeOriginChainID(chainOid)
	return chainID, txid
}

// composeSequencerChainOriginTx is the pure wasm-wallet compose helper for the
// sequencer-chain-origin tx. The produced tx is a REGULAR WALLET TX (no `s` bit,
// no SequencerData slot) that emits a chain-origin output carrying:
//   - the 2-arg sequencer constraint at slot 4 (with epochSlots, maxFrozenEpochs);
//   - optional sequencer milestone data at slot 5 (when initialSeqData != nil).
//
// The easyfl `sequencer` constraint skips the milestone-index check when the
// sibling chain constraint is an origin, so SetSequencerData is intentionally NOT
// called. No endorsements either — the origin reaches the tangle via its
// tag-along output.
func composeSequencerChainOriginTx(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	target ledger.Lock,
	onChainAmount uint64,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	ts base.LedgerTime,
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
	// chain origin: coverageDelta starts at 0 (origins are exempt from the
	// strict-increase rule; the first milestone sets the real value).
	seqBin, err := lib.NewSequencerConstraintBytecode(epochSlots, maxFrozenEpochs, 0)
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
	// No SetSequencerData call: the tx is a regular wallet tx producing a chain-origin
	// output. The easyfl `sequencer` constraint skips the milestone-index check when the
	// chain constraint is an origin (selfChainPredInputIndex == 0x). No endorsements
	// either — the origin is naturally pulled into the tangle via its tag-along output.
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, 0, nil, err
	}
	return txBytes, txid, chainOutIdx, consumed, nil
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
