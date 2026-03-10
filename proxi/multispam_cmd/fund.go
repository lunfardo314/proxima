package multispam_cmd

import (
	"fmt"
	"time"

	"crypto/ed25519"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuilder"
	"github.com/lunfardo314/proxima/multispam"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// Max targets per transaction: 256 outputs minus tag-along (1) minus remainder (1) = 254
const maxTargetsPerTx = 254

func initFundCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fund",
		Short: "fund sender accounts from wallet",
		Args:  cobra.NoArgs,
		Run:   runFundCmd,
	}
	cmd.Flags().Uint64("amount", 0, "amount of tokens to send to each sender (required)")
	_ = cmd.MarkFlagRequired("amount")
	cmd.Flags().StringSlice("senders", nil, "fund only named senders (comma-separated or repeated, default: all)")
	return cmd
}

func runFundCmd(cmd *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()

	configFile := viper.GetString("multispam-config")
	cfg, err := multispam.LoadConfig(configFile)
	glb.AssertNoError(err)

	amount, _ := cmd.Flags().GetUint64("amount")
	if amount == 0 {
		glb.Fatalf("--amount must be > 0")
	}

	senderNames, _ := cmd.Flags().GetStringSlice("senders")

	// Resolve target senders
	senders := cfg.Senders
	if len(senderNames) > 0 {
		senders = filterSenders(cfg.Senders, senderNames)
	}

	walletData := glb.GetWalletData()
	clnt := glb.GetClient()

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	tagAlongFee := glb.GetTagAlongFee()
	if tagAlongFee == 0 {
		tagAlongFee = 1
	}

	// Resolve sender addresses
	type target struct {
		name string
		addr ledger.Lock
	}
	targets := make([]target, len(senders))
	for i, s := range senders {
		addr, err := multispam.SenderAddress(s.KeyFile)
		glb.AssertNoError(err)
		targets[i] = target{name: s.Name, addr: addr}
	}

	glb.Infof("funding %d senders with %d tokens each from wallet", len(targets), amount)

	// Split into batches that fit within max outputs per tx
	for batchStart := 0; batchStart < len(targets); batchStart += maxTargetsPerTx {
		batchEnd := batchStart + maxTargetsPerTx
		if batchEnd > len(targets) {
			batchEnd = len(targets)
		}
		batch := targets[batchStart:batchEnd]

		totalNeeded := amount*uint64(len(batch)) + tagAlongFee
		walletAccount := ledger.SigLockFromED25519PrivateKey(walletData.PrivateKey)
		walletOutputs, _, _, err := clnt.GetOutputsForAmount(walletAccount, totalNeeded)
		glb.AssertNoError(err)

		// Build multi-output transaction
		txb := txbuilder.New()
		inTotal, inTs, err := txb.ConsumeOutputsNoUnlock(walletOutputs...)
		glb.AssertNoError(err)

		ts := ledger.TimeNow()
		glb.Assertf(ledger.ValidTransactionPace(inTs, ts), "timestamp pace violation, try again shortly")

		// Set unlock params: signature on input 0, references for rest
		for i := range walletOutputs {
			if i == 0 {
				txb.PutSignatureUnlock(0)
			} else {
				_ = txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			}
		}

		// Produce one output per target
		for _, t := range batch {
			out := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(amount).WithLock(t.addr)
			})
			_, err = txb.ProduceOutput(out)
			glb.AssertNoError(err)
		}

		// Produce tag-along output
		holderID := base.HolderIDFromPublicKey(base.SignatureTypeED25519, walletData.PrivateKey.Public().(ed25519.PublicKey))
		tagAlongOut := ledger.NewTagAlongOutput(tagAlongFee, *tagAlongSeqID, holderID)
		_, err = txb.ProduceOutput(tagAlongOut)
		glb.AssertNoError(err)

		// Produce remainder if needed
		spent := amount*uint64(len(batch)) + tagAlongFee
		if inTotal > spent {
			remainderOut := ledger.NewOutput(func(o *ledger.OutputBuilder) {
				o.WithTokenBalance(inTotal - spent).WithLock(walletAccount)
			})
			_, err = txb.ProduceOutput(remainderOut)
			glb.AssertNoError(err)
		}

		txb.TransactionData.Timestamp = ts
		txb.TransactionData.InputCommitment = ledger.HashOutputs(txb.ConsumedOutputs...)
		txb.SignED25519(walletData.PrivateKey)

		txBytes, txid, txString, err := txb.BytesWithValidation()
		if err != nil {
			glb.Fatalf("transaction validation failed: %v\n%s", err, txString)
		}

		err = clnt.SubmitTransaction(txBytes)
		glb.AssertNoError(err)

		for _, t := range batch {
			glb.Infof("  %s: %d tokens", t.name, amount)
		}
		glb.Infof("submitted tx %s (%d targets, %d total tokens)", txid.StringShort(), len(batch), amount*uint64(len(batch)))

		included := glb.TrackTxInclusion(txid, time.Second)
		if !included {
			glb.Fatalf("transaction %s not included, aborting", txid.StringShort())
		}
		fmt.Println()
	}

	glb.Infof("funding complete")
}

func filterSenders(all []multispam.SenderConfig, names []string) []multispam.SenderConfig {
	nameSet := make(map[string]bool, len(names))
	for _, n := range names {
		nameSet[n] = true
	}
	var result []multispam.SenderConfig
	for _, s := range all {
		if nameSet[s.Name] {
			result = append(result, s)
			delete(nameSet, s.Name)
		}
	}
	for n := range nameSet {
		glb.Fatalf("sender '%s' not found in config", n)
	}
	return result
}
