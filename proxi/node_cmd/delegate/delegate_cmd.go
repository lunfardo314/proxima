package delegate

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func Init() *cobra.Command {
	delegationCmd := &cobra.Command{
		Use:     "delegate",
		Aliases: []string{"dlg"},
		Short:   `defines subcommands for the delegation function`,
		Args:    cobra.NoArgs,
	}

	delegationCmd.AddCommand(
		initDelegateAmountCmd(),
		initDelegationTopUpCmd(),
		initRevokeDelegationCmd(),
		initDelegationStatusCmd(),
		initDelegationSubmitCmd(),
		initTargetInfoCmd(),
		initEstimateCmd(),
	)

	delegationCmd.InitDefaultHelpCmd()
	return delegationCmd
}

// addFlagCut registers the delegator (inflation) cut flag: the share of a
// delegation's inflation the delegator requires, below which the target
// sequencer may not freeze it. --cut and --minimum_cut are synonyms.
//
// The default is left at 0 here and resolved by delegatorCut when the command
// runs, because the wallet profile that carries 'delegate.minimum_cut' is only
// read in PersistentPreRun, after flag registration.
func addFlagCut(cmd *cobra.Command) {
	cmd.Flags().Uint16("cut", 0, "delegator (inflation) cut in promille (0-1000); default: delegate.minimum_cut from the wallet profile")
	cmd.Flags().Uint16("minimum_cut", 0, "synonym of --cut")
}

// delegatorCut resolves the delegator (inflation) cut for this run: whichever
// of the synonymous flags was given, or the wallet profile value. Giving both
// is only accepted when they agree, so a typo cannot silently pick one.
func delegatorCut(cmd *cobra.Command) uint16 {
	cut, _ := cmd.Flags().GetUint16("cut")
	minimumCut, _ := cmd.Flags().GetUint16("minimum_cut")
	switch {
	case cmd.Flags().Changed("cut") && cmd.Flags().Changed("minimum_cut"):
		glb.Assertf(cut == minimumCut, "--cut and --minimum_cut are synonyms and must agree")
	case cmd.Flags().Changed("minimum_cut"):
		cut = minimumCut
	case !cmd.Flags().Changed("cut"):
		cut = glb.GetMinimumDelegatorCut()
	}
	glb.Assertf(cut <= 1000, "delegator cut must be 0-1000 promille")
	return cut
}
