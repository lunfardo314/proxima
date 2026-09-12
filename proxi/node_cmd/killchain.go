package node_cmd

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/set"
	"github.com/spf13/cobra"
)

func initKillChainCmd() *cobra.Command {
	deleteChainCmd := &cobra.Command{
		Use:     "killchain <chain id> [<chain id> ...]",
		Aliases: []string{"endchain, delchain"},
		Short:   `ends chains by destroying their chain outputs, all in one transaction. All tokens are converted into one addressED25519-locked output with the same controlling private key`,
		Args:    cobra.MinimumNArgs(1),
		Run:     runKillChainCmd,
	}
	deleteChainCmd.InitDefaultHelpCmd()

	return deleteChainCmd
}

func runKillChainCmd(_ *cobra.Command, args []string) {
	chainIDs := make([]base.ChainID, 0, len(args))
	seen := set.New[base.ChainID]()
	for _, arg := range args {
		chainID, err := base.ChainIDFromHexString(arg)
		glb.AssertNoError(err)
		glb.Assertf(!seen.Contains(chainID), "chain %s is listed twice", chainID.String())
		seen.Insert(chainID)
		chainIDs = append(chainIDs, chainID)
	}
	// each chain output is one input, indexed by a byte
	glb.Assertf(len(chainIDs) < 256, "at most 255 chains can be discontinued in one transaction")

	walletData := glb.GetWalletData()

	tagAlongSeqIDPtr := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqIDPtr != nil, "tag-along sequencer not specified")
	tagAlongSeqID := *tagAlongSeqIDPtr

	clnt := glb.GetClient()
	feeAmount, err := glb.GetRequiredTagAlongFee(tagAlongSeqID)
	glb.AssertNoError(err)
	glb.Assertf(feeAmount > 0, "tag-along fee must be > 0")

	// Wallet-derived "now" — singleton-free.
	consts := glb.GetLedgerConstants()
	lib := glb.GetTxLibrary()
	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}

	outs := make([]*ledger.OutputWithChainID, 0, len(chainIDs))
	var total uint64
	for _, chainID := range chainIDs {
		out, _, err := clnt.GetChainOutput(chainID)
		glb.AssertNoError(err)

		// Delegation frozen-slot UX guard. If this is a delegation output
		// in a frozen slot, the master cannot unlock it — bail with a
		// helpful message rather than submit a tx that the server will
		// reject. Pure wallet-side parse via lib.ParseDelegationOutput +
		// Constants epoch math.
		if view, isDelegation, err := lib.ParseDelegationOutput(out.Output.Output, out.ID); err != nil {
			glb.AssertNoError(err)
		} else if isDelegation && view.IsInFrozenSlot(ts.Slot, consts) {
			unfreeze := view.UnfreezeSlot(consts)
			glb.Infof("in the current slot %d the delegation output %s cannot be unlocked by the master lock because it is frozen until slot %d",
				ts.Slot, chainID.StringShort(), unfreeze)
			glb.Infof("safe revocation window is %d slots from now: slots %d - %d",
				unfreeze-ts.Slot, ts.Slot, unfreeze)
			return
		}
		glb.Infof("chain %s: balance %s", chainID.String(), util.Th(out.Output.TokenBalance()))
		ts = base.MaximumTime(ts, out.Timestamp())
		total += out.Output.TokenBalance()
		outs = append(outs, out)
	}
	glb.Assertf(total > feeAmount, "total chain balance %s does not cover tag-along fee %s", util.Th(total), util.Th(feeAmount))

	var prompt string
	if len(chainIDs) == 1 {
		prompt = fmt.Sprintf("discontinue chain %s?", chainIDs[0].String())
	} else {
		short := make([]string, len(chainIDs))
		for i, chainID := range chainIDs {
			short[i] = chainID.StringShort()
		}
		prompt = fmt.Sprintf("discontinue %d chains %s in one transaction?", len(chainIDs), strings.Join(short, ", "))
	}
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	txb := txbuildercore.New(0)

	// Every chain output is consumed under the transaction signature. A reference unlock
	// would not do: it is only valid for the plain sigLock, and a delegation's master path
	// needs its own unlock bytes.
	consumedBytes := make([][]byte, 0, len(outs))
	for i, out := range outs {
		chainInBytes := out.Output.Bytes()
		txb.ConsumeOutput(chainInBytes, out.ID)
		consumedBytes = append(consumedBytes, chainInBytes)

		// Master-unlock byte (0xff) satisfies the delegation lock's master
		// path; ignored by plain sigLock-chain outputs.
		txb.PutSignatureUnlock(byte(i), ledger.DelegationUnlockedByMaster)
		// FinishChainUnlockParams (empty) discontinues the chain at slot 3.
		txb.PutUnlockParams(byte(i), ledger.ConstraintIndexChain, txbuildercore.FinishChainUnlockParams)
	}

	// Sweep all funds back to the wallet under sigLock (minus tag-along fee).
	sweepOut, err := txbuildercore.NewSigLockOutput(lib, total-feeAmount, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(sweepOut.Bytes())

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)
	glb.Infof("submitting transaction %s", txid.String())
	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	if !glb.NoWait() {
		glb.TrackTxInclusion(txid, time.Second)
	}
}
