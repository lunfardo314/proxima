package node_cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/transaction"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"
)

type fundTarget struct {
	Target string `yaml:"target"`
	Amount uint64 `yaml:"amount"`
}

func initFundCmd() *cobra.Command {
	fundCmd := &cobra.Command{
		Use:   "fund",
		Short: "sends tokens to multiple targets in one transaction",
		Long: `Reads a YAML file with a list of targets (controller lock source and amount)
and sends the specified amounts in a single transaction.

Example distribute.yaml:
  - target: "sigLock(0xabcdef...)"
    amount: 1000000
  - target: "chainLock(0x123456...)"
    amount: 2000000`,
		Args: cobra.NoArgs,
		Run:  runFundCmd,
	}

	fundCmd.PersistentFlags().String("targets", "distribute.yaml", "YAML file with target list")
	err := viper.BindPFlag("fund.targets", fundCmd.PersistentFlags().Lookup("targets"))
	glb.AssertNoError(err)

	fundCmd.InitDefaultHelpCmd()
	return fundCmd
}

func runFundCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()

	targetsFile := viper.GetString("fund.targets")
	data, err := os.ReadFile(targetsFile)
	glb.AssertNoError(err)

	var targets []fundTarget
	glb.AssertNoError(yaml.Unmarshal(data, &targets))
	glb.Assertf(len(targets) > 0, "no targets specified in %s", targetsFile)

	// Parse all targets and compute total
	type parsedTarget struct {
		lock   ledger.Lock
		amount uint64
	}
	parsed := make([]parsedTarget, len(targets))
	totalAmount := uint64(0)
	for i, t := range targets {
		ctrl, err := ledger.ControllerFromSource(t.Target)
		glb.Assertf(err == nil, "target #%d: %v", i, err)
		parsed[i] = parsedTarget{lock: ctrl.AsLock(), amount: t.Amount}
		totalAmount += t.Amount
	}

	// Tag-along fee
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0")

	md, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)
	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}

	// Number of outputs: targets + fee + possible remainder
	// Max 256 outputs in a transaction
	maxTargets := 256 - 2 // fee + remainder
	glb.Assertf(len(parsed) <= maxTargets, "too many targets (%d), maximum is %d per transaction", len(parsed), maxTargets)

	walletData := glb.GetWalletData()
	walletAccount := walletData.Account

	glb.Infof("source: %s", walletAccount.String())
	glb.Infof("targets file: %s (%d targets)", targetsFile, len(parsed))
	glb.Infof("total to distribute: %s", util.Th(totalAmount))
	glb.Infof("tag-along fee: %s to %s", util.Th(feeAmount), tagAlongSeqID.StringShort())
	for i, t := range parsed {
		glb.Infof("  #%d: %s -> %s", i, util.Th(t.amount), t.lock.String())
	}

	if !glb.YesNoPrompt("Proceed?", true) {
		os.Exit(0)
	}

	// Fetch inputs
	walletOutputs, _, _, err := glb.GetClient().GetOutputsForAmount(walletAccount, totalAmount+feeAmount)
	glb.AssertNoError(err)

	// Build transaction
	txb := txbuilder.New()
	inTotal, inTs, err := txb.ConsumeOutputsNoUnlock(walletOutputs...)
	glb.AssertNoError(err)

	ts := ledger.TimeNow()
	glb.Assertf(ledger.ValidTransactionPace(inTs, ts), "wrong time constraints")
	glb.Assertf(inTotal >= totalAmount+feeAmount, "not enough balance: have %s, need %s", util.Th(inTotal), util.Th(totalAmount+feeAmount))

	// Unlock inputs
	for i := range walletOutputs {
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			_ = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
		}
	}

	// Produce target outputs
	for _, t := range parsed {
		out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(t.amount).WithLock(t.lock)
		})
		_, err = txb.ProduceOutput(out)
		glb.AssertNoError(err)
	}

	// Tag-along fee output
	tagAlongOut := ledger.NewTagAlongOutput(feeAmount, *tagAlongSeqID, base.HolderID(walletAccount))
	_, err = txb.ProduceOutput(tagAlongOut)
	glb.AssertNoError(err)

	// Remainder
	if inTotal > totalAmount+feeAmount {
		remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
			o.WithTokenBalance(inTotal - totalAmount - feeAmount).WithLock(walletAccount)
		})
		_, err = txb.ProduceOutput(remainderOut)
		glb.AssertNoError(err)
	}

	txb.TransactionData.Timestamp = ts
	txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
	txb.SignED25519(walletData.PrivateKey)

	txBytes, _, txString, err := txb.BytesWithValidation()
	if err != nil {
		glb.Fatalf("%v\n------ failing transaction -------\n%s", err, txString)
	}

	tx, err := transaction.ParseWithPartialValidation(txBytes)
	glb.AssertNoError(err)
	err = tx.SetFullContext(tx.InputLoaderByIndex(transaction.PickOutputFromListFunc(walletOutputs)))
	glb.AssertNoError(err)

	glb.Verbosef("-------- fund transaction ---------\n%s\n----------------", fmt.Sprintf("%s", tx.String()))

	err = glb.GetClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)
	glb.Infof("transaction %s submitted successfully", tx.IDShortString())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(tx.ID(), time.Second)
}
