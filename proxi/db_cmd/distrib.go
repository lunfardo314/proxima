package db_cmd

import (
	"strconv"

	"github.com/lunfardo314/proxima/core/txmetadata"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initDBDistributeCmd() *cobra.Command {
	dbDistributeCmd := &cobra.Command{
		Use:   "distribute",
		Short: "creates initial distribution of genesis branch. Arguments must be a list of pairs <lockSource> <balance>",
		Run:   runDBDistributeCmd,
	}
	dbDistributeCmd.InitDefaultHelpCmd()
	return dbDistributeCmd
}

func runDBDistributeCmd(_ *cobra.Command, args []string) {
	glb.Assertf(len(args) > 0 && len(args)%2 == 0, "even-sized list of arguments is expected")

	glb.InitLedger()
	glb.InitTxStoreDB()
	defer glb.CloseDatabases()

	distribution := make([]ledger.LockBalance, len(args)/2)
	var err error
	for i := 0; i < len(args); i += 2 {
		distribution[i/2].Lock, err = ledger.AddressED25519FromSource(args[i])
		glb.Assertf(err == nil, "%v in argument %d", err, i)
		distribution[i/2].Balance, err = strconv.ParseUint(args[i+1], 10, 64)
		glb.Assertf(err == nil, "%v in argument %d", err, i)
	}

	stateID, _, err := multistate.ScanGenesisState(glb.StateStore())
	glb.AssertNoError(err)

	glb.Infof("Re-check the distribution list:")
	totalToDistribute := uint64(0)
	for i := range distribution {
		glb.Infof("%s -> %s", util.GoTh(distribution[i].Balance), distribution[i].Lock.String())
		glb.Assertf(stateID.InitialSupply-distribution[i].Balance > totalToDistribute, "wrong distribution sum")
		totalToDistribute += distribution[i].Balance
	}
	glb.Infof("Total to distribute: %s", util.GoTh(totalToDistribute))
	glb.Infof("Total initial supply: %s", util.GoTh(stateID.InitialSupply))
	glb.Infof("Will remain on origin account: %s", util.GoTh(stateID.InitialSupply-totalToDistribute))

	if !glb.YesNoPrompt("Continue?", false) {
		glb.Infof("Exit. Genesis state hasn't been modified")
		return
	}

	walletData := glb.GetWalletData()
	txBytes, err := txbuilder.DistributeInitialSupply(glb.StateStore(), walletData.PrivateKey, distribution)
	glb.AssertNoError(err)

	txID, err := transaction.IDFromTransactionBytes(txBytes)
	glb.AssertNoError(err)

	rr, found := multistate.FetchRootRecord(glb.StateStore(), txID)
	glb.Assertf(found, "inconsistency: can't find branch")

	glb.Infof("New branch has been created. Distribution transaction ID: %s", txID.String())
	fname := txID.AsFileName()
	glb.Infof("Saving distribution transaction to the file '%s'", fname)
	err = transaction.SaveTransactionAsFile(txBytes, fname)
	glb.AssertNoError(err)
	glb.Infof("Success")

	glb.Infof("Storing transaction into transaction store")

	_, err = glb.TxBytesStore().PersistTxBytesWithMetadata(txBytes, &txmetadata.TransactionMetadata{
		StateRoot:           rr.Root,
		LedgerCoverageDelta: util.Ref(rr.LedgerCoverage.LatestDelta()),
		SlotInflation:       util.Ref(rr.SlotInflation),
		Supply:              util.Ref(rr.Supply),
	})
	glb.AssertNoError(err)
	glb.Infof("Success")
}
