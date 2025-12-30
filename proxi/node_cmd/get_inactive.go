package node_cmd

import (
	"bytes"
	"sort"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
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

	lrbRootRecord, _, err := glb.GetClient().GetLatestReliableBranch()
	glb.AssertNoError(err)

	slotsBack := 360
	if len(args) > 0 {
		slotsBack, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
	}
	outs, err := glb.GetClient().GetInactiveUTXOs(slotsBack)
	glb.AssertNoError(err)

	glb.Infof("\n-- current slot: %d", ledger.SlotNow())
	glb.Infof("-- inactive UTXOs since slot %d:\n", outs.SinceSlot)

	type oBin struct {
		oid     base.OutputID
		lockStr string
		amount  uint64
		utxoStr string
	}

	outs1 := make([]*oBin, 0)
	for _, o := range outs.UTXOs {
		id, err := base.OutputIDFromHexString(o.ID)
		glb.AssertNoError(err)
		outs1 = append(outs1, &oBin{
			oid:     id,
			lockStr: o.Lock,
			amount:  o.Amount,
			utxoStr: o.OutputString,
		})
	}
	sort.Slice(outs1, func(i, j int) bool {
		return bytes.Compare(outs1[i].oid[:], outs1[j].oid[:]) < 0
	})
	total := uint64(0)
	for _, o := range outs1 {
		glb.Infof("%75s: %s      %15s", o.oid.String(), o.lockStr, util.Th(o.amount))
		glb.Verbosef("%s\n", o.utxoStr)
		total += o.amount
	}

	glb.Infof("----------\ntotal inactive: %s (%.2f%% of total supply)", util.Th(total), 100*float32(total)/float32(lrbRootRecord.Supply))

	type addrTotal struct {
		utxos int
		total uint64
	}
	byAddr := make(map[string]addrTotal)

	for _, o := range outs1 {
		r := byAddr[o.lockStr]
		r.utxos += 1
		r.total += o.amount
		byAddr[o.lockStr] = r
	}
	glb.Infof("------------\nby address:")
	for addr, t := range byAddr {
		glb.Infof("%25s: utxos: %d, total: %s", addr, t.utxos, util.Th(t.total))
	}
}
