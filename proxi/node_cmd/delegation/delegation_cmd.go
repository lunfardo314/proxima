package delegation

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
		initDelegationSendCmd(),
		initRevokeDelegationCmd(),
		initDelegationStatusCmd(),
		initDelegationSubmitCmd(),
	)

	delegationCmd.InitDefaultHelpCmd()
	return delegationCmd
}
