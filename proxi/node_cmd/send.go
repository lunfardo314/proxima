package node_cmd

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

// `proxi node send` — wallet-side single-output transfer.
//
// Target syntax (-t / --target):
//
//   a/<32-byte hex>   — sigLock target (the legacy default; -t HEX with no
//                       prefix is also still accepted by ControllerFromSource
//                       and treated as sigLock).
//   c/<32-byte hex>   — chainLock target. The produced output is locked
//                       under the standard chainLock, spendable by the
//                       controller of the given chainID.
//
// Modes:
//
//   plain (default)        — produce a sigLock or chainLock output, depending
//                            on -t. The output is immediately spendable by
//                            the target.
//   --deadline             — produce a sendWithDeadline output. The target
//                            (sigLock OR chainLock) has --acceptance-slots
//                            slots to claim; after that, the wallet
//                            (master) has the configured reclaim window;
//                            after --cleanup-slots, anyone can purge.
//
// `proxi node transfer` is kept as a deprecated alias that delegates to
// `send` so existing scripts keep working.

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
	glb.InitLedgerFromNode()

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

	// Tagged native-token transfer is a separate flow (see
	// send_tagged.go). It builds the tx by hand: tokenAmount inputs +
	// PRXI funding + tokenAmount outputs + Phase D token() sentinel.
	if tagHex != "" {
		glb.Assertf(!deadlineMode, "--tag is incompatible with --deadline")
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		runSendTaggedCmd(amount, tagHex)
		return
	}

	wallet := glb.GetWalletData()
	glb.Infof("source: wallet account %s", wallet.Account.String())

	targetCtrl := glb.MustGetTarget()

	// Resolve tag-along.
	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified (set tag_along.sequencer_id)")
	feeAmount := glb.GetTagAlongFee()
	glb.Assertf(feeAmount > 0, "tag-along fee not configured (set tag_along.fee)")

	md, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
	glb.AssertNoError(err)
	if md.MinimumFee() > feeAmount {
		feeAmount = md.MinimumFee()
	}

	// Build the final Lock to put on the output.
	var targetLock ledger.Lock
	if deadlineMode {
		targetLock = buildSendWithDeadlineLock(wallet, targetCtrl, acceptanceSlots, cleanupSlots)
		glb.Infof("mode:   sendWithDeadline (acceptance=%d slots, cleanup=%d slots)",
			acceptanceSlots, cleanupSlots)
		glb.Infof("target: %s", describeSWDTarget(targetCtrl))
	} else {
		// Reject deadline-only flags on the plain path.
		glb.Assertf(!cmd.Flags().Changed("acceptance-slots"),
			"--acceptance-slots only applies with --deadline")
		glb.Assertf(!cmd.Flags().Changed("cleanup-slots"),
			"--cleanup-slots only applies with --deadline")
		targetLock = targetCtrl
		glb.Infof("mode:   plain transfer (target lock is %s)", targetCtrl.Name())
		glb.Infof("target: %s", targetCtrl.String())
	}

	prompt := fmt.Sprintf("send will cost %s of fees paid to tag-along sequencer %s. Proceed?",
		util.Th(feeAmount), tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	txCtx, err := glb.GetClient().TransferFromED25519Wallet(client.TransferFromED25519WalletParams{
		WalletPrivateKey: wallet.PrivateKey,
		TagAlongSeqID:    tagAlongSeqID,
		TagAlongFee:      feeAmount,
		Amount:           amount,
		Target:           targetLock,
	})
	if txCtx != nil {
		glb.Verbosef("-------- send transaction ---------\n%s\n----------------", txCtx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(txCtx != nil, "inconsistency: txCtx == nil")
	glb.Infof("transaction submitted successfully")

	if glb.NoWait() {
		return
	}
	glb.TrackTxInclusion(txCtx.ID(), time.Second)
}

// =============================================================================
// helpers
// =============================================================================

// buildSendWithDeadlineLock turns the parsed -t Controller into a
// SendWithDeadlineLock. The master is the wallet's holderID; the target
// is derived from the Controller kind:
//
//   sigLock target   → targetType=0x00, targetID = holder ID
//   chainLock target → targetType=0x01, targetID = chain ID
func buildSendWithDeadlineLock(wallet glb.WalletData, target ledger.Controller, acceptanceSlots, cleanupSlots uint32) *ledger.SendWithDeadlineLock {
	masterID := base.HolderID(ledger.SigLockFromED25519PrivateKey(wallet.PrivateKey))

	var targetID base.HolderID
	var targetType byte
	switch c := target.(type) {
	case ledger.SigLock:
		copy(targetID[:], c[:])
		targetType = ledger.SendWithDeadlineTargetSigLock
	case ledger.ChainLock:
		// ChainLock is a 32-byte []byte holding a chain ID.
		glb.Assertf(len(c) == 32, "ChainLock controller must carry a 32-byte chain ID, got %d bytes", len(c))
		copy(targetID[:], c)
		targetType = ledger.SendWithDeadlineTargetChainLock
	default:
		glb.Assertf(false, "--deadline only supports sigLock or chainLock targets, got %s", target.Name())
	}
	return &ledger.SendWithDeadlineLock{
		MasterID:        masterID,
		TargetID:        targetID,
		TargetType:      targetType,
		AcceptanceSlots: acceptanceSlots,
		CleanupSlots:    cleanupSlots,
	}
}

// describeSWDTarget returns a human-readable form like
// "a/<hex>" or "c/<hex>" describing the target controller.
func describeSWDTarget(target ledger.Controller) string {
	return target.Source()
}
