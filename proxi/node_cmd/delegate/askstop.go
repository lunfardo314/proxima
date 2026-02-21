package delegate

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initRevokeDelegationCmd() *cobra.Command {
	revokeCmd := &cobra.Command{
		Use:     "askstop <delegation ID>",
		Aliases: util.List("stop"),
		Short:   "send 'stop delegation' request to the target sequencer with the given delegation ID",
		Args:    cobra.ExactArgs(1),
		Run:     runRevokeDelegationCmd,
	}

	glb.AddFlagTarget(revokeCmd)

	revokeCmd.InitDefaultHelpCmd()
	return revokeCmd
}

func runRevokeDelegationCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	delegationID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	clnt := glb.GetClient()
	out, _, err := clnt.GetChainOutput(delegationID)
	glb.AssertNoError(err)
	dOut, ok := ledger.AsDelegationOutput(out.Output, out.ID)
	glb.Assertf(ok, "not a delegation output:\n%s", out.String())
	if glb.IsVerbose() {
		glb.Infof("delegation output:\n%s", out.String())
	}

	glb.Assertf(dOut.MasterID == base.SpenderID(walletData.Account), "this wallet is not a master controller of the delegation %s", delegationID.String())

	targetID := dOut.Target
	glb.Infof("delegation target ID: %s", targetID.String())

	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(5)
	}
	glb.Assertf(!dOut.IsUnlockableByMaster(ts.Slot), "delegation is unlockable by master, no need for revocation")
	unfreeze := dOut.UnfreezeSlot()
	glb.Assertf(unfreeze > uint32(ts.Slot)+6, "delegation is not frozen or safe revocation window is very close, just wait up to a minute")

	compensation := dOut.RevocationCompensationEstimate(ts.Slot)
	const minimumFee = 50

	glb.Assertf(compensation >= minimumFee, "estimated compensation is even less than minimum fee %d", minimumFee)

	requestOutput := txbuilder_seq.NewAskStopDelegationReqOutput(targetID, walletData.Account, delegationID, compensation)

	txBytes, txid, txString, err := glb.GetClient().MakeSendOutputTransaction(requestOutput, walletData.PrivateKey, ts)
	if err != nil {
		glb.Infof("error: %v", err)
		if txString != "" {
			glb.Infof("------------ failing tx --------------\n" + txString)
		}
		return
	}
	prompt := fmt.Sprintf("send request to stop delegation %s to the sequencer %s?", delegationID.StringShort(), targetID.String())
	if !glb.YesNoPrompt(prompt, true) {
		return
	}
	err = clnt.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	glb.TrackTxInclusion(txid, time.Second)
}
