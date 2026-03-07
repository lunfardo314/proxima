package seq_cmd

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/seqdata"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initSeqSetCmd() *cobra.Command {
	setCmd := &cobra.Command{
		Use:   "set-params",
		Short: `update sequencer parameters (name, fee, margin, greedy, pace, ignore-freeze-bound)`,
		Args:  cobra.NoArgs,
		Run:   runSeqSetCmd,
	}

	glb.AddFlagTarget(setCmd)

	setCmd.Flags().String("name", "", "sequencer name")
	setCmd.Flags().Uint64("fee", 0, "minimum tag-along fee")
	setCmd.Flags().Uint16("margin", 0, "inflation profit margin promille (0-1000)")
	setCmd.Flags().Bool("greedy", false, "greedy flag")
	setCmd.Flags().Uint8("pace", 0, "pace value (ticks)")
	setCmd.Flags().Bool("ignore-freeze-bound", false, "ignore upper bound on freeze")

	setCmd.InitDefaultHelpCmd()
	return setCmd
}

func runSeqSetCmd(cmd *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	walletData := glb.GetWalletData()
	glb.Assertf(walletData.Sequencer != nil, "can't get own sequencer id")

	seqID := *walletData.Sequencer
	glb.Infof("sequencer id: %s", seqID.String())

	// fetch current sequencer data
	clnt := glb.GetClient()
	seqOut, _, err := clnt.GetChainOutput(seqID)
	glb.AssertNoError(err)

	currentSD, err := ledger.ParseSequencerData(seqOut.Output)
	if err != nil {
		currentSD = seqdata.SequencerData{}
	}

	// apply only explicitly changed flags
	// clear proposer strategy — it is set internally by the sequencer, not by the user
	currentSD.SetProposerStrategy("")
	newSD := currentSD.Clone()
	changed := false

	if cmd.Flags().Changed("name") {
		v, _ := cmd.Flags().GetString("name")
		glb.Assertf(len(v) <= 6, "name must be empty (reset to default) or 1 to 6 characters")
		newSD.SetName(v)
		changed = true
	}
	if cmd.Flags().Changed("fee") {
		v, _ := cmd.Flags().GetUint64("fee")
		newSD.SetMinimumFee(v)
		changed = true
	}
	if cmd.Flags().Changed("margin") {
		v, _ := cmd.Flags().GetUint16("margin")
		glb.Assertf(v <= 1000, "margin must be 0-1000")
		newSD.SetSeqProfitMarginPromille(v)
		changed = true
	}
	if cmd.Flags().Changed("greedy") {
		v, _ := cmd.Flags().GetBool("greedy")
		newSD.SetGreedy(v)
		changed = true
	}
	if cmd.Flags().Changed("pace") {
		v, _ := cmd.Flags().GetUint8("pace")
		newSD.SetPace(v)
		changed = true
	}
	if cmd.Flags().Changed("ignore-freeze-bound") {
		v, _ := cmd.Flags().GetBool("ignore-freeze-bound")
		newSD.SetIgnoreFreezeBound(v)
		changed = true
	}

	if !changed {
		glb.Infof("no flags specified, nothing to change")
		glb.Infof("current sequencer data:\n%s", currentSD.Lines("  ").String())
		return
	}

	glb.Infof("current:\n%s", currentSD.Lines("  ").String())
	glb.Infof("new:\n%s", newSD.Lines("  ").String())

	// get the minimum tag-along fee from the sequencer
	fee, err := glb.GetRequiredTagAlongFee(seqID)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(fee))

	tagAlongOut := txbuilder_seq.NewSeqDataCommandOutput(seqID, walletData.Account, fee, newSD)
	ts := ledger.TimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(12)
	}
	txBytes, txid, txString, err := clnt.MakeSendOutputTransaction(tagAlongOut, walletData.PrivateKey, ts)
	if err != nil {
		glb.Infof("error: %s", err)
		if txString != "" {
			glb.Infof("------------ failing tx ---------------\n" + txString)
		}
		return
	}

	glb.Verbosef("---- request transaction ------\n%s\n------------------", txString)
	prompt := fmt.Sprintf("\nupdate sequencer %s parameters?", seqID.String())
	if !glb.YesNoPrompt(prompt, true) {
		return
	}
	glb.Infof("submitting the transaction...")

	err = clnt.SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}
