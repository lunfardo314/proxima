package node_cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initTransferCmd() *cobra.Command {
	transferCmd := &cobra.Command{
		Use:   "transfer <amount>",
		Short: `sends tokens from the wallet's account to the target`,
		Args:  cobra.ExactArgs(1),
		Run:   runTransferCmd,
	}

	transferCmd.PersistentFlags().StringP("target", "t", "", "target lock in EasyFL source format")
	err := viper.BindPFlag("target", transferCmd.PersistentFlags().Lookup("target"))
	glb.AssertNoError(err)

	transferCmd.InitDefaultHelpCmd()
	return transferCmd
}

func runTransferCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	walletData := glb.GetWalletData()

	glb.Infof("source is the wallet account: %s", walletData.Account.String())
	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	target := glb.MustGetTarget()

	var tagAlongSeqID *ledger.ChainID
	feeAmount := getTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		md, err := glb.GetClient().GetMilestoneDataFromHeaviestState(*tagAlongSeqID)
		glb.AssertNoError(err)

		if md != nil && md.MinimumFee > feeAmount {
			feeAmount = md.MinimumFee
		}
	}

	var prompt string
	if feeAmount > 0 {
		prompt = fmt.Sprintf("transfer will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.StringShort())
	} else {
		prompt = "transfer transaction will not have tag-along fee output (fee-less). Proceed?"
	}
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := glb.GetClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
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
	//glb.AssertNoError(waitForInclusion(txCtx.OutputID(0)))
}

// TODO take into account vertex status
func waitForInclusion(oid ledger.OutputID, timeout ...time.Duration) error {
	glb.Infof("Tracking inclusion of %s:", oid.StringShort())
	startTime := time.Now()
	var deadline time.Time
	if len(timeout) > 0 {
		deadline = startTime.Add(timeout[0])
	} else {
		deadline = startTime.Add(2 * time.Minute)
	}
	time.Sleep(1 * time.Second)

	var inclusionData []api.InclusionData
	var err error

	util.DoUntil(func() {
		inclusionData, err = glb.GetClient().GetOutputInclusion(&oid)
		glb.AssertNoError(err)

		displayInclusionState(inclusionData, time.Since(startTime).Seconds())
	}, func() bool {
		// TODO not 100% correct because depends on the number of active sequencers
		_, percOfTotal, percOfDominating := glb.InclusionScore(inclusionData, ledger.DefaultInitialSupply)
		if percOfTotal == 100 || percOfDominating == 100 {
			glb.Infof("full inclusion reached in %v", time.Since(startTime))
			return true
		}
		if time.Now().After(deadline) {
			err = fmt.Errorf("waitForInclusion: timeout")
			return true
		}
		time.Sleep(1 * time.Second)
		return false
	})
	return err
}
