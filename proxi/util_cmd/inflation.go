package util_cmd

import (
	"fmt"
	"strconv"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func initInflationCmd() *cobra.Command {
	initLedgerIDCmd := &cobra.Command{
		Use:   "inflation <amount> [<n slots>] [<start slot, default-current>]",
		Args:  cobra.RangeArgs(1, 3),
		Short: fmt.Sprintf("inflation calculator: for the start amount and start slot for number of slots"),
		PersistentPreRun: func(_ *cobra.Command, _ []string) {
			glb.ReadInConfig()
		},
		Run: runInflationCmd,
	}
	initLedgerIDCmd.PersistentFlags().StringP("config", "c", "", "profile name")
	err := viper.BindPFlag("config", initLedgerIDCmd.PersistentFlags().Lookup("config"))
	glb.AssertNoError(err)

	return initLedgerIDCmd
}

func runInflationCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	amount := uint64(amountInt)
	slots := uint32(1)
	if len(args) > 1 {
		slotsInt, err := strconv.Atoi(args[1])
		glb.AssertNoError(err)
		glb.Assertf(slots >= 1, "wrong number of slots")
		slots = uint32(slotsInt)
	}
	currentSlot := ledger.SlotNow()
	startSlot := currentSlot
	if len(args) > 2 {
		startInt, err := strconv.Atoi(args[2])
		glb.AssertNoError(err)
		glb.Assertf(startInt >= 0, "wrong start slot")
		startSlot = uint32(startInt)
	}

	inflation := ledger.L(base.MaxSlot).ChainInflationMultiStep(amount, startSlot, slots)
	glb.Infof("--------------------")
	glb.Infof("current slot:    %d", currentSlot)
	glb.Infof("start slot:      %d", startSlot)
	glb.Infof("number of slots: %d", slots)
	glb.Infof("start amount:    %s", util.Th(amount))
	glb.Infof("inflation:       %s", util.Th(inflation))
	glb.Infof("final amount:    %s", util.Th(amount+inflation))
}
