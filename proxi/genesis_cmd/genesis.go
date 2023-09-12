package genesis_cmd

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/core"
	"github.com/lunfardo314/proxima/genesis"
	"github.com/lunfardo314/proxima/proxi/console"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

var (
	supply      uint64
	description string
	nowis       time.Time
)

func InitGenesisCmd(rootCmd *cobra.Command) {
	genesisCmd := &cobra.Command{
		Use:   "genesis <dbname> [--supply <supply>] [--desc 'description']",
		Short: "create genesis ledger state database and transaction store database",
		Args:  cobra.ExactArgs(1),
		Run:   runGenesis,
	}
	nowis = time.Now()
	genesisCmd.Flags().Uint64Var(&supply, "supply", genesis.DefaultSupply, fmt.Sprintf("initial supply (default is %s", util.GoThousands(genesis.DefaultSupply)))
	defaultDesc := fmt.Sprintf("genesis has been created at Unix time (nanoseconds) %d", nowis.UnixNano())
	genesisCmd.Flags().StringVar(&description, "desc", defaultDesc, fmt.Sprintf("default is '%s'", defaultDesc))

	rootCmd.AddCommand(genesisCmd)
}

func runGenesis(_ *cobra.Command, args []string) {
	console.Infof("Creating genesis ledger state...")
	console.Infof("Multi-state database : %s", args[0])
	console.Infof("Transaction store database : %s", args[0]+".txstore")
	console.Infof("Initial supply: %s", util.GoThousands(supply))
	console.Infof("Description: '%s'", description)
	nowisTs := core.LogicalTimeFromTime(nowis)
	console.Infof("Genesis time slot: %d", nowisTs.TimeSlot())
}
