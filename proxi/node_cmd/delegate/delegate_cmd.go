package delegate

import (
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
		initRevokeDelegationCmd(),
		initDelegationStatusCmd(),
		initDelegationSubmitCmd(),
		initTargetInfoCmd(),
	)

	delegationCmd.InitDefaultHelpCmd()
	return delegationCmd
}
