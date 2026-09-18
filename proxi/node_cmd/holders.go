package node_cmd

import (
	"fmt"
	"sort"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	holdersSortByIdle bool
	holdersMaxUTXOs   int
)

// The node answers only when its configuration enables get_holders: the
// scan walks the whole UTXO set of the latest reliable branch.
func initHoldersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "holders",
		Short: `lists, per holder, the total and the idle capital in the latest reliable branch. Idle is neither in a sequencer chain nor frozen in a delegation`,
		Args:  cobra.NoArgs,
		Run:   runHoldersCmd,
	}
	cmd.Flags().BoolVarP(&holdersSortByIdle, "idle", "i", false, "sort by idle capital instead of total capital")
	cmd.Flags().IntVarP(&holdersMaxUTXOs, "max_utxos", "m", 0, "scan at most that many UTXOs. The node's own cap still applies")
	cmd.InitDefaultHelpCmd()
	return cmd
}

const holdersRowFormat = "%-64s %7s %24s %24s %8s"

func runHoldersCmd(_ *cobra.Command, _ []string) {
	var maxUTXOs []int
	if holdersMaxUTXOs > 0 {
		maxUTXOs = []int{holdersMaxUTXOs}
	}
	res, err := glb.GetClient().GetHolders(maxUTXOs...)
	glb.AssertNoError(err)

	lrbid, err := base.TransactionIDFromHexString(res.LRBID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	type row struct {
		holder string
		api.HolderTotals
	}
	rows := make([]row, 0, len(res.Holders))
	for holder, t := range res.Holders {
		rows = append(rows, row{holder: holder, HolderTotals: t})
	}
	sort.Slice(rows, func(i, j int) bool {
		ki, kj := rows[i].Total, rows[j].Total
		if holdersSortByIdle {
			ki, kj = rows[i].Idle, rows[j].Idle
		}
		if ki != kj {
			return ki > kj
		}
		return rows[i].holder < rows[j].holder
	})

	printRow := func(name string, t api.HolderTotals) {
		glb.Infof(holdersRowFormat, name, fmt.Sprintf("%d", t.NumOutputs), prox(t.Total), prox(t.Idle),
			fmt.Sprintf("%.2f%%", 100*float64(t.Total)/float64(res.Supply)))
	}

	glb.Infof(holdersRowFormat, "holder id", "n", "total", "idle", "share")
	var sum api.HolderTotals
	for _, r := range rows {
		sum.NumOutputs += r.NumOutputs
		sum.Total += r.Total
		sum.Idle += r.Idle
		printRow(r.holder, r.HolderTotals)
	}
	glb.Infof("----------")
	printRow(fmt.Sprintf("%d holders", len(rows)), sum)
	printRow("other locks", res.Other)
	glb.Infof(holdersRowFormat, "supply", "", prox(res.Supply), "", "")
	if res.Truncated {
		glb.Infof("WARNING: the scan stopped after %s UTXOs, the totals cover only part of the state",
			util.Th(res.NumScanned, ","))
	}
}

// prox renders motes as PROX with the full six decimals
func prox(motes uint64) string {
	return fmt.Sprintf("%s.%06d", util.Th(motes/base.PROX, ","), motes%base.PROX)
}
