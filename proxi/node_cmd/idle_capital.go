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

// Operator's tool, deliberately hidden from the help and the docs.
func initIdleCapitalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "idle_capital",
		Short:  `totals, per lock, the capital in the latest reliable branch that is neither in a sequencer chain nor delegated`,
		Hidden: true,
		Args:   cobra.NoArgs,
		Run:    runIdleCapitalCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runIdleCapitalCmd(_ *cobra.Command, _ []string) {
	res, err := glb.GetClient().GetIdleCapital()
	glb.AssertNoError(err)

	lrbid, err := base.TransactionIDFromHexString(res.LRBID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	type row struct {
		lock string
		api.AccountTotals
	}
	rows := make([]row, 0, len(res.Accounts))
	for lock, t := range res.Accounts {
		rows = append(rows, row{lock: lock, AccountTotals: t})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Balance != rows[j].Balance {
			return rows[i].Balance > rows[j].Balance
		}
		return rows[i].lock < rows[j].lock
	})

	var numOutputs int
	var total uint64
	for _, r := range rows {
		numOutputs += r.NumOutputs
		total += r.Balance
		glb.Infof("%-66s %6d outputs %26s PROX  %6.2f%%", r.lock, r.NumOutputs, prox(r.Balance), 100*float64(r.Balance)/float64(res.Supply))
	}
	glb.Infof("----------\n%d locks, %d outputs, idle capital %s PROX (%.2f%% of supply %s PROX)",
		len(rows), numOutputs, prox(total), 100*float64(total)/float64(res.Supply), prox(res.Supply))
}

// prox renders motes as PROX with the full six decimals
func prox(motes uint64) string {
	return fmt.Sprintf("%s.%06d", util.Th(motes/base.PROX, ","), motes%base.PROX)
}
