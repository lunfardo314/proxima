package node_cmd

import (
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var chainCmdDecompilePolicy bool

func initChainCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:   "chain <chainID, hex-encoded>",
		Short: `displays details of the specific chain`,
		Args:  cobra.ExactArgs(1),
		Run:   runChainCmd,
	}
	glb.AddFlagTarget(getBalanceCmd)
	getBalanceCmd.PersistentFlags().BoolVarP(&chainCmdDecompilePolicy, "decompile", "D", false,
		"if the chain is a foundry, also print its policy script (if any) in decompiled EasyFL source form")
	err := viper.BindPFlag("decompile", getBalanceCmd.PersistentFlags().Lookup("decompile"))
	glb.AssertNoError(err)
	getBalanceCmd.InitDefaultHelpCmd()
	return getBalanceCmd
}

func runChainCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	out, lrbid, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	dOut, isDelegation := ledger.AsDelegationOutput(out.Output, out.ID)
	seqData, isSequencer := out.Output.SequencerOutputData()

	cc := out.Output.ChainConstraint()
	isFoundry := false
	var foundry *ledger.Foundry
	if fBytes, err := out.Output.ConstraintAt(ledger.ConstraintIndexFoundry); err == nil {
		if f, err := ledger.FoundryFromBytes(fBytes); err == nil {
			isFoundry = true
			foundry = f
		}
	}

	glb.Infof("\nCHAIN OUTPUT DATA:\n-----------------")
	glb.Infof("chain ID:             %s", chainID.String())
	glb.Infof("output ID:            %s", out.ID.String())
	glb.Infof("token balance:        %s", util.Th(out.Output.TokenBalance()))
	glb.Infof("is delegation output: %v", isDelegation)
	glb.Infof("is sequencer output:  %v", isSequencer)
	glb.Infof("is foundry output:    %v", isFoundry)
	glb.Infof("is branch output:     %v", out.ID.IsBranchTransaction())
	if cc != nil {
		glb.Infof("origin slot:          %d", cc.OriginSlot)
		glb.Infof("transition counter:   %d", cc.TransitionCounter)
		glb.Infof("cumulative inflation: %s (chain: %s, branch bonus: %s)",
			util.Th(cc.CumulativeChainInflation+cc.CumulativeBranchBonus),
			util.Th(cc.CumulativeChainInflation), util.Th(cc.CumulativeBranchBonus))
	}
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
		glb.Infof("DELEGATION OUTPUT DATA (current slot is %d):\n-----------------", ledger.SlotNow())
		glb.Infof("%s", dOut.LinesDelegationData().String())
	}

	if isFoundry {
		printFoundryDetails(out, foundry, chainID)
	}
}

// printFoundryDetails renders the FOUNDRY DATA section for `proxi node
// chain <chainID>` when the resolved output is a foundry. Shows tag,
// circulating supply, the optional policy script at index 5 with a
// recognised description, and (when -D / --decompile is set) the
// decompiled EasyFL source of the policy.
func printFoundryDetails(out *ledger.OutputWithChainID, f *ledger.Foundry, chainID base.ChainID) {
	glb.Infof("FOUNDRY DATA:\n-----------------")
	// Tag at origin is still NilChainID; the real chain ID is derivable
	// from the output ID and equals the resolved chainID we already have.
	displayTag := f.Tag
	if displayTag == base.NilChainID {
		glb.Infof("foundry tag:          %s  (origin, will become %s at first transit)",
			f.Tag.String(), chainID.String())
	} else {
		glb.Infof("foundry tag:          %s", f.Tag.String())
	}
	glb.Infof("circulating supply:   %s", util.Th(f.Supply))

	var policy []byte
	if p, err := out.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy); err == nil {
		policy = p
	}
	glb.Infof("policy:               %s", policyDescriptionLine(policy))
	if len(policy) > 0 {
		glb.Infof("policy bytes:         %d", len(policy))
		if chainCmdDecompilePolicy {
			printDecompiledPolicySource(policy, "    ")
		}
	}
	glb.Infof("\n")
}
