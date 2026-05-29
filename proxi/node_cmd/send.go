package node_cmd

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// `proxi node send` — wallet-side single-output transfer.
//
// Target syntax (-t / --target):
//
//   a/<32-byte hex>   — sigLock target.
//   c/<32-byte hex>   — chainLock target. The produced output is locked
//                       under the standard chainLock, spendable by the
//                       controller of the given chainID.
//
// Modes:
//
//   plain (default)        — produce a sigLock or chainLock output, depending
//                            on -t. Immediately spendable by the target.
//   --deadline             — produce a sendWithDeadline output. The target
//                            (sigLock OR chainLock) has --acceptance-slots
//                            slots to claim; after that, this wallet (master)
//                            has the configured reclaim window;
//                            after --cleanup-slots, anyone can purge.
//   --tag <chainID-hex>    — native-token transfer of that tag instead of
//                            (or in addition to) PRXI. Delegates to
//                            send_tagged.go. Incompatible with --deadline.

const (
	defaultAcceptanceSlots uint32 = 60
	defaultCleanupSlots    uint32 = 8000
)

func initSendCmd() *cobra.Command {
	sendCmd := &cobra.Command{
		Use:   "send <amount>",
		Short: "send tokens from the wallet to a sigLock holder or a chainLock chain",
		Long: `Send <amount> tokens to a target identified by -t / --target.

Target syntax:
  a/<32-byte hex>   sigLock target — the produced output is locked to the
                    holder whose ED25519 holderID == that 32-byte value.
  c/<32-byte hex>   chainLock target — the output is locked under the
                    standard chainLock, spendable by the controller of
                    the given chainID.

Pass --deadline to produce a sendWithDeadline output instead of a plain
sigLock/chainLock output. The target then has --acceptance-slots to claim
the funds; after that, this wallet can reclaim until --cleanup-slots,
after which anyone can purge the output (see
claude/send_with_deadline_lock.md).

Pass --tag <chainID-hex> to transfer native tokens of that tag instead
of (or in addition to) PRXI. The recipient output gains a
tokenAmount(<tag>, <amount>) constraint; the tx pushes a sentinel
token(<tag>, 0x) for Phase D auditability and Σ-conservation. The wallet
must hold sufficient tokenAmount(<tag>, _) UTXOs to cover <amount>; any
remainder is returned as a new tokenAmount UTXO. --tag is incompatible
with --deadline.`,
		Args: cobra.ExactArgs(1),
		Run:  runSendCmd,
	}
	glb.AddFlagTarget(sendCmd)
	sendCmd.Flags().Bool("deadline", false, "produce a sendWithDeadline output instead of plain sigLock/chainLock")
	sendCmd.Flags().Uint32("acceptance-slots", defaultAcceptanceSlots,
		fmt.Sprintf("target's acceptance window in slots (only with --deadline; min %d)",
			ledger.SendWithDeadlineMinAcceptanceSlots))
	sendCmd.Flags().Uint32("cleanup-slots", defaultCleanupSlots,
		fmt.Sprintf("cleanup boundary in slots (only with --deadline; must exceed acceptance by ≥ %d)",
			ledger.SendWithDeadlineMinReclaimSlots))
	sendCmd.Flags().String("tag", "",
		"native-token tag (foundry chain ID, hex); transfer <amount> tokens of this tag instead of PRXI")
	sendCmd.InitDefaultHelpCmd()
	return sendCmd
}

func runSendCmd(cmd *cobra.Command, args []string) {
	amount, err := strconv.ParseUint(args[0], 10, 64)
	glb.AssertNoError(err)

	deadlineMode, err := cmd.Flags().GetBool("deadline")
	glb.AssertNoError(err)
	acceptanceSlots, err := cmd.Flags().GetUint32("acceptance-slots")
	glb.AssertNoError(err)
	cleanupSlots, err := cmd.Flags().GetUint32("cleanup-slots")
	glb.AssertNoError(err)
	tagHex, err := cmd.Flags().GetString("tag")
	glb.AssertNoError(err)

	// Tagged native-token transfer is a separate flow (see send_tagged.go).
	if tagHex != "" {
		glb.Assertf(!deadlineMode, "--tag is incompatible with --deadline")
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		runSendTaggedCmd(amount, tagHex)
		return
	}
	if !deadlineMode {
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
	}

	wallet := glb.GetWalletData()
	glb.Infof("source: wallet account %s", wallet.Account.String())

	targetCtrl := glb.MustGetTarget()

	// manage tag along data

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")

	seqMinFee, err := glb.GetSequencerMinimumFee(*tagAlongSeqID)
	glb.AssertNoError(err)

	feeAmount := glb.GetTagAlongFee()
	if seqMinFee > feeAmount {
		// assume fee asked by the sequencer
		feeAmount = seqMinFee
	}
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")

	// Wallet-derived "now" — wall-clock mapped through the genesis +
	// tick-duration constants. Singleton-free equivalent of
	// ledger.TimeNow().Slot.
	targetSlot := glb.GetLedgerTimeNow().Slot

	// Build the recipient output for whichever mode is selected. Both
	// branches go through txbuildercore + the wallet helpers; no
	// ledger.NewOutput / ledger/txbuilder sugar reachable from here.
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(wallet.PrivateKey)

	var targetOut *txbuildercore.Output
	if deadlineMode {
		targetOut, err = buildSendWithDeadlineOutput(lib, targetCtrl, walletHolderID, amount, acceptanceSlots, cleanupSlots)
		glb.AssertNoError(err)
		glb.Infof("mode:   sendWithDeadline (acceptance=%d slots, cleanup=%d slots)",
			acceptanceSlots, cleanupSlots)
		glb.Infof("target: %s", targetCtrl.Source())
	} else {
		targetOut, err = glb.BuildLockOutput(lib, amount, targetCtrl)
		glb.AssertNoError(err)
		glb.Infof("mode:   plain transfer (target lock is %s)", targetCtrl.Name())
		glb.Infof("target: %s", targetCtrl.String())
	}

	// Fetch sigLock-owned wallet inputs covering amount + fee.
	needed := amount + feeAmount
	res, err := glb.GetClient().GetOutputsForControllerID(wallet.Account.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.Assertf(res.AvailableAmount >= needed,
		"not enough tokens: have %s, need %s", util.Th(res.AvailableAmount), util.Th(needed))

	prompt := fmt.Sprintf("send will cost %s of fees paid to tag-along sequencer %s. Proceed?",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txBytes, txid, consumed, err := makeSendTransaction(
		wallet.PrivateKey, res.Outputs, targetOut, amount,
		*tagAlongSeqID, feeAmount, targetSlot)
	glb.AssertNoError(err)
	glb.Assertf(txBytes != nil, "something wrong: empty send tx")

	if err := glb.SubmitAndDisplay(txBytes, consumed...); err != nil {
		os.Exit(1)
	}
	glb.Infof("transaction %s submitted successfully", txid.StringShort())

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txid, time.Second)
}

// buildSendWithDeadlineOutput composes the recipient SWD output from
// the parsed -t Controller. The master is the wallet's holderID; the
// target is derived from the Controller kind (sigLock holder bytes
// for sigLock targets, raw chainID bytes for chainLock targets).
func buildSendWithDeadlineOutput(
	lib *txbuildercore.Library[any],
	targetCtrl ledger.Controller,
	masterID base.HolderID,
	amount uint64,
	acceptanceSlots, cleanupSlots uint32,
) (*txbuildercore.Output, error) {
	var (
		targetID   base.HolderID
		targetType byte
	)
	switch c := targetCtrl.(type) {
	case ledger.SigLock:
		copy(targetID[:], c[:])
		targetType = txbuildercore.SendWithDeadlineTargetSigLock
	case ledger.ChainLock:
		if len(c) != 32 {
			return nil, fmt.Errorf("--deadline chainLock target must carry a 32-byte chain ID, got %d", len(c))
		}
		copy(targetID[:], c)
		targetType = txbuildercore.SendWithDeadlineTargetChainLock
	default:
		return nil, fmt.Errorf("--deadline only supports sigLock or chainLock targets, got %s", targetCtrl.Name())
	}
	return lib.NewSendWithDeadlineOutput(txbuildercore.SendWithDeadlineOutputParams{
		Amount:          amount,
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      targetType,
		AcceptanceSlots: acceptanceSlots,
		CleanupSlots:    cleanupSlots,
	})
}

// makeSendTransaction is the pure wasm-wallet compose helper for
// `proxi node send`: consumes the supplied sigLock-owned wallet
// inputs and produces the recipient output, the tag-along fee output
// and an optional remainder back to the wallet. No I/O; no
// ledger.L() singleton; no ledger/txbuilder sugar.
//
// Input unlock pattern: PutSignatureUnlock(0) on input 0 (carries
// the tx signature) + PutUnlockReference(i, ConstraintIndexLock, 0)
// on the rest. The reference path makes the on-chain `_sigLock`
// constraint short-circuit through `unlockedByReference` for the
// homogeneous inputs 1..N — same holderID + same lock bytecode —
// skipping one txHolderID(...) hash compare per referenced input.
func makeSendTransaction(
	walletPrivateKey ed25519.PrivateKey,
	walletOutputs []*ledger.OutputWithID,
	targetOut *txbuildercore.Output,
	targetAmount uint64,
	tagAlongSeqID base.ChainID,
	tagAlongFee uint64,
	targetSlot uint32,
) (txBytes []byte, txid base.TransactionID, consumed [][]byte, err error) {
	lib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletPrivateKey)
	txb := txbuildercore.New(0)

	inTotal := uint64(0)
	consumed = make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumed = append(consumed, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			if err = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0); err != nil {
				return nil, base.TransactionID{}, nil, err
			}
		}
		inTotal += in.Output.TokenBalance()
	}
	if inTotal < targetAmount+tagAlongFee {
		return nil, base.TransactionID{}, nil, fmt.Errorf("not enough balance: have %d, need %d",
			inTotal, targetAmount+tagAlongFee)
	}

	txb.ProduceOutput(targetOut.Bytes())

	taOut, err := txbuildercore.NewTagAlongOutput(lib, tagAlongFee, tagAlongSeqID, walletHolderID)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	txb.ProduceOutput(taOut.Bytes())

	if inTotal > targetAmount+tagAlongFee {
		remainderOut, rerr := txbuildercore.NewSigLockOutput(lib, inTotal-targetAmount-tagAlongFee, walletHolderID)
		if rerr != nil {
			return nil, base.TransactionID{}, nil, rerr
		}
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(base.T(targetSlot, 10))
	txb.ComputeInputCommitment()
	txb.SignED25519(walletPrivateKey)

	txBytes = txb.Bytes()
	txid, err = txbuildercore.TxIDFromBytes(txBytes)
	if err != nil {
		return nil, base.TransactionID{}, nil, err
	}
	return txBytes, txid, consumed, nil
}
