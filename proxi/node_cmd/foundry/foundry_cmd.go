package foundry

import "github.com/spf13/cobra"

// Init returns the `proxi node foundry ...` subcommand tree. Future
// subcommands (mint / burn / retire) attach here.
func Init() *cobra.Command {
	foundryCmd := &cobra.Command{
		Use:   "foundry",
		Short: "subcommands for native-token foundries (claude/native_token.md)",
		Args:  cobra.NoArgs,
	}
	foundryCmd.AddCommand(
		initFoundryCreateCmd(),
		initFoundryMintCmd(),
		initFoundryBurnCmd(),
		initFoundryRetireCmd(),
	)
	foundryCmd.InitDefaultHelpCmd()
	return foundryCmd
}
