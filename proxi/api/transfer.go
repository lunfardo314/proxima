package api

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initTransferCmd(apiCmd *cobra.Command) {
	transferCmd := &cobra.Command{
		Use:   "transfer",
		Short: `sends tokens from the wallet's account to the target`,
		Args:  cobra.ExactArgs(1),
		Run:   runTransferCmd,
	}

	transferCmd.InitDefaultHelpCmd()
	apiCmd.AddCommand(transferCmd)
}

func runTransferCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("source is the wallet account: %s", walletData.Account.String())
	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	target := glb.MustGetTarget()

	var tagAlongSeqID *core.ChainID
	feeAmount := getTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := getClient().GetMilestoneDataFromHeaviestState(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("transfer will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.Short())
	} else {
		prompt = "transfer transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := getClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: walletData.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           target.AsLock(),
	})
	if txCtx != nil {
		glb.Verbosef("-------- transfer transaction ---------\n%s\n----------------", txCtx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(txCtx != nil, "inconsistency: txCtx == nil")
	glb.Infof("transaction submitted successfully")

	if NoWait() {
		return
	}
	waitForInclusion(txCtx.OutputID(0))
}

func allIncluded(incl []api.InclusionData) bool {
	glb.Assertf(len(incl) > 0, "len(incl)>0")

	for i := range incl {
		if !incl[i].Included {
			return false
		}
	}
	return true
}

func waitForInclusion(oid core.OutputID) {
	glb.Infof("Tracking inclusion of %s:", oid.Short())
	startTime := time.Now()
	time.Sleep(1 * time.Second)

	var inclusionData []api.InclusionData
	var err error

	util.DoUntil(func() {
		glb.Infof("Inclusion state in %.1f seconds:", time.Since(startTime).Seconds())
		inclusionData, err = getClient().GetOutputInclusion(&oid)
		glb.AssertNoError(err)

		displayInclusionState(inclusionData)
	}, func() bool {
		if allIncluded(inclusionData) {
			glb.Infof("full inclusion reached")
			return true
		}
		time.Sleep(1 * time.Second)
		return false
	})
}
