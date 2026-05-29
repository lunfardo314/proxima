package node_cmd

import (
	"os"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
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
		parsed[i] = parsedTarget{lock: ctrl, amount: t.Amount}
		totalAmount += t.Amount
	}

	// Tag-along fee
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0")

	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)
	if seqMinFee > feeAmount {
		feeAmount = seqMinFee
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
	needed := totalAmount + feeAmount
	res, err := glb.GetClient().GetOutputsForControllerID(walletAccount.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.Assertf(res.AvailableAmount >= needed, "not enough tokens: have %s, need %s", util.Th(res.AvailableAmount), util.Th(needed))
	walletOutputs := res.Outputs

	// Wasm-style build via txbuildercore + helpers.
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)

	// Track max input timestamp for pace validation. ledger.ValidTransactionPace
	// inlines as: tx_ts - max(in_ts) ≥ TransactionPace ticks.
	inTs := base.NilLedgerTime
	for _, in := range walletOutputs {
		inTs = base.MaximumTime(inTs, in.Timestamp())
	}
	ts := glb.GetLedgerTimeNow()
	glb.Assertf(base.DiffTicks(ts, inTs) >= int64(consts.TransactionPace), "wrong time constraints")

	txb := txbuildercore.New(0)

	consumedBytes := make([][]byte, 0, len(walletOutputs))
	inTotal := uint64(0)
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		inTotal += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}
	glb.Assertf(inTotal >= totalAmount+feeAmount, "not enough balance: have %s, need %s", util.Th(inTotal), util.Th(totalAmount+feeAmount))

	// Produce target outputs (sigLock or chainLock based on target type).
	for i, t := range parsed {
		out, err := glb.BuildLockOutput(lib, t.amount, t.lock)
		glb.Assertf(err == nil, "target #%d (%s): %v", i, t.lock.String(), err)
		txb.ProduceOutput(out.Bytes())
	}

	// Tag-along.
	tagAlongOut, err := txbuildercore.NewTagAlongOutput(lib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	// Remainder back to wallet.
	if inTotal > totalAmount+feeAmount {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, inTotal-totalAmount-feeAmount, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)
	glb.Infof("transaction %s submitted successfully", txid.String())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}
