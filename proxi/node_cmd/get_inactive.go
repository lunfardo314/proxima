package node_cmd

import (
	"strconv"

	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initGetInactiveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get_inactive [<slots back>]",
		Short: `displays UTXO inactive since given slot (default is 360 slots back)`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runGetInactiveCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runGetInactiveCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	var err error

	slotsBack := 360
	if len(args) > 0 {
		slotsBack, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
	}
	outs, err := glb.GetClient().GetInactiveUTXOs(slotsBack)
	glb.AssertNoError(err)

	for _, o := range outs.UTXOs {
		glb.Infof("\n%s     %s", o.ID, o.Lock)
	}
}
