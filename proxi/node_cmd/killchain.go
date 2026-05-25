package node_cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initKillChainCmd() *cobra.Command {
	deleteChainCmd := &cobra.Command{
		Use:     "killchain <chain id>",
		Aliases: []string{"endchain, delchain"},
		Short:   `ends a chain by destroying chain output. All tokens are converted into the addressED25519-locked output with the same controlling private key`,
		Args:    cobra.ExactArgs(1),
		Run:     runKillChainCmd,
	}
	deleteChainCmd.InitDefaultHelpCmd()

	return deleteChainCmd
}

func runKillChainCmd(_ *cobra.Command, args []string) {
	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	walletData := glb.GetWalletData()

	tagAlongSeqIDPtr := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqIDPtr != nil, "tag-along sequencer not specified")
	tagAlongSeqID := *tagAlongSeqIDPtr

	clnt := glb.GetClient()
	seqMinFee, err := glb.GetSequencerMinimumFee(tagAlongSeqID)
	glb.AssertNoError(err)

	feeAmount := glb.GetTagAlongFee()
	if seqMinFee > feeAmount {
		feeAmount = seqMinFee
	}
	glb.Assertf(feeAmount > 0, "tag-along fee must be > 0")

	prompt := fmt.Sprintf("discontinue chain %s?", chainID.String())
	if !glb.YesNoPrompt(prompt, true, glb.BypassYesNoPrompt()) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// GetChainOutputData returns the raw output bytes + ID without going
	// through ledger.ChainConstraintFromBytesWithLib (which assumes the
	// ledger.L() singleton). The chain-constraint parse is not needed here —
	// we only consume the output and read its token balance — so we keep
	// the wallet path singleton-free by parsing structurally with
	// ledger.OutputFromBytes (library-free) and going through the wallet
	// library only for the delegation guard below.
	outData, _, err := clnt.GetChainOutputData(chainID)
	glb.AssertNoError(err)
	parsedOut, err := ledger.OutputFromBytes(outData.Data)
	glb.AssertNoError(err)

	// Wallet-derived "now" — singleton-free.
	consts := glb.GetLedgerConstants()
	ts := consts.LedgerTimeFromClockTime(time.Now())
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}

	// Delegation frozen-slot UX guard. If this is a delegation output
	// in a frozen slot, the master cannot unlock it — bail with a
	// helpful message rather than submit a tx that the server will
	// reject. Pure wallet-side parse via lib.ParseDelegationOutput +
	// Constants epoch math.
	lib := glb.GetTxLibrary()
	if view, isDelegation, err := lib.ParseDelegationOutput(parsedOut.Output, outData.ID); err != nil {
		glb.AssertNoError(err)
	} else if isDelegation && view.IsInFrozenSlot(ts.Slot, consts) {
		unfreeze := view.UnfreezeSlot(consts)
		glb.Infof("in the current slot %d the delegation output cannot be unlocked by the master lock because it is frozen until slot %d",
			ts.Slot, unfreeze)
		glb.Infof("safe revocation window is %d slots from now: slots %d - %d",
			unfreeze-ts.Slot, ts.Slot, unfreeze)
		return
	}

	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	txb := txbuildercore.New(0)

	// Consume the chain output as input 0.
	chainInBytes := outData.Data
	txb.ConsumeOutput(chainInBytes, outData.ID)
	consumedBytes := [][]byte{chainInBytes}

	// Master-unlock byte (0xff) satisfies the delegation lock's master
	// path; ignored by plain sigLock-chain outputs.
	txb.PutSignatureUnlock(0, ledger.DelegationUnlockedByMaster)
	// FinishChainUnlockParams (empty) discontinues the chain at slot 3.
	txb.PutUnlockParams(0, ledger.ConstraintIndexChain, txbuildercore.FinishChainUnlockParams)

	// Sweep all funds back to the wallet under sigLock (minus tag-along fee).
	chainBal := parsedOut.TokenBalance()
	glb.Assertf(chainBal > feeAmount, "chain balance %s does not cover tag-along fee %s", chainBal, feeAmount)
	sweepOut, err := txbuildercore.NewSigLockOutput(lib, chainBal-feeAmount, walletHolderID)
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
