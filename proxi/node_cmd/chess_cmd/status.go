package chess_cmd

import (
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status <chainID>",
		Short: "print the current chess game state (board + metadata) from the LRB",
		Args:  cobra.ExactArgs(1),
		Run:   runStatusCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runStatusCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	chainID := parseChainID(args[0])
	fetchAndPrintBoard(chainID, "chess game status")
}
