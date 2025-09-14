package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initChainCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:   "chain <chainID, hex-encoded>",
		Short: `displays details of the specific chain`,
		Args:  cobra.ExactArgs(1),
		Run:   runChainCmd,
	}
	glb.AddFlagTarget(getBalanceCmd)
	getBalanceCmd.InitDefaultHelpCmd()
	return getBalanceCmd
}

func runChainCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	out, _, lrbid, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	dOut, isDelegation := ledger.AsDelegationOutput(out.Output, out.ID)
	seqData, isSequencer := out.Output.SequencerOutputData()

	glb.Infof("\nCHAIN OUTPUT DATA:\n-----------------")
	glb.Infof("chain ID:             %s", chainID.String())
	glb.Infof("output ID:            %s", out.ID.String())
	glb.Infof("token balance:        %s", util.Th(out.Output.TokenBalance()))
	glb.Infof("is delegation output: %v", isDelegation)
	glb.Infof("is sequencer output:  %v", isSequencer)
	glb.Infof("is branch output:     %v", out.ID.IsBranchTransaction())
	if glb.IsVerbose() {
		glb.Infof("constraints:\n%s", out.Output.LinesHR("      "))
	}
	glb.Infof("\n")
	if isSequencer {
		glb.Infof("SEQUENCER DATA:\n-----------------")
		glb.Infof("%s", seqData.SequencerData.Lines("    ").String())
		glb.Infof("\n")
	}

	if isDelegation {
		glb.Infof("delegation output: true")
		glb.Infof("DELEGATION OUTPUT DATA (slot now is %d):\n-----------------", ledger.SlotNow())
		glb.Infof("%s", dOut.LinesDelegationData().String())
	}
}
