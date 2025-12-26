package node_cmd

import (
	"bytes"
	"sort"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
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

	glb.Infof("\ncurrent slot: %d", ledger.SlotNow())
	glb.Infof("inactive UTXOs since slot %d:\n", outs.SinceSlot)

	type oBin struct {
		oid     base.OutputID
		lockStr string
	}

	outs1 := make([]*oBin, 0)
	for _, o := range outs.UTXOs {
		id, err := base.OutputIDFromHexString(o.ID)
		glb.AssertNoError(err)
		outs1 = append(outs1, &oBin{oid: id, lockStr: o.Lock})
	}
	sort.Slice(outs1, func(i, j int) bool {
		return bytes.Compare(outs1[i].oid[:], outs1[j].oid[:]) < 0
	})
	for _, o := range outs1 {
		glb.Infof("%75s: %s", o.oid.String(), o.lockStr)
	}

}
