package node_cmd

import (
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var scanSigLocksAll bool

// Operator's tool, deliberately hidden from the help and the docs.
func initScanSigLocksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "scan_siglocks",
		Short:  `lists accounts holding sigLock outputs in the latest reliable branch, with balance and number of outputs`,
		Hidden: true,
		Args:   cobra.NoArgs,
		Run:    runScanSigLocksCmd,
	}
	cmd.Flags().BoolVar(&scanSigLocksAll, "all", false, "include outputs under every lock type, not only sigLock")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runScanSigLocksCmd(_ *cobra.Command, _ []string) {
	res, err := glb.GetClient().GetAccounts()
	glb.AssertNoError(err)

	lrbid, err := base.TransactionIDFromHexString(res.LRBID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	// The server keys every lock by its string form; a sigLock reads as 'a/<hex>'.
	sigLockPrefix := ledger.SigLock{}.String()[:2]

	type row struct {
		lock string
		api.AccountTotals
	}
	rows := make([]row, 0, len(res.Accounts))
	var numUTXOs int
	var totalAll uint64
	for lock, t := range res.Accounts {
		numUTXOs += t.NumOutputs
		totalAll += t.Balance
		if scanSigLocksAll || strings.HasPrefix(lock, sigLockPrefix) {
			rows = append(rows, row{lock: lock, AccountTotals: t})
		}
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
		glb.Infof("%-66s %6d outputs %22s  %6.2f%%", r.lock, r.NumOutputs, util.Th(r.Balance), 100*float64(r.Balance)/float64(res.Supply))
	}
	if scanSigLocksAll {
		glb.Infof("----------\n%d locks, %d outputs, total %s", len(rows), numOutputs, util.Th(total))
	} else {
		glb.Infof("----------\n%d sigLock accounts, %d outputs, total %s (%.2f%% of supply)",
			len(rows), numOutputs, util.Th(total), 100*float64(total)/float64(res.Supply))
	}
	glb.Infof("state: %d outputs, total %s, supply %s", numUTXOs, util.Th(totalAll), util.Th(res.Supply))
}
