package node_cmd

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

const (
	defaultMaxNumberOfInputs = 100
)

func initCompactOutputsCmd() *cobra.Command {
	compactCmd := &cobra.Command{
		Use:   "compact [<max number of args. Default 100, maximum allowed 256>]",
		Short: `compacts up to <max number> non-chain outputs in the wallet account into one ED25519 output`,
		Args:  cobra.MaximumNArgs(1),
		Run:   runCompactCmd,
	}
	compactCmd.InitDefaultHelpCmd()
	return compactCmd
}

func runCompactCmd(_ *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	maxNumberOfInputs := defaultMaxNumberOfInputs
	var err error
	if len(args) > 0 {
		maxNumberOfInputs, err = strconv.Atoi(args[0])
		glb.AssertNoError(err)
		glb.Assertf(2 <= maxNumberOfInputs && maxNumberOfInputs <= 256, "parameter must be >= 2 and <= 256")
	}

	var tagAlongSeqID *base.ChainID
	feeAmount := glb.GetTagAlongFee()
	if feeAmount > 0 {
		tagAlongSeqID = glb.GetTagAlongSequencerID()
		glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")

		sd, err := glb.GetClient().GetSequencerData(*tagAlongSeqID)
		glb.AssertNoError(err)

		if sd.MinimumFee() > feeAmount {
			feeAmount = sd.MinimumFee()
		}
	}
	walletData := glb.GetWalletData()
	res, err := glb.GetClient().GetOutputs(walletData.Account.ControllerID(), client.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   client.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderAsc,
	})
	glb.AssertNoError(err)
	// Restrict to "basic" sigLock outputs (amounts | index-values | lock,
	// no extras like timelock); these are unlockable with a plain
	// signature/reference and safe to compact in one transaction.
	walletOutputs := util.PurgeSlice(res.Outputs, func(o *ledger.OutputWithID) bool {
		return o.Output.NumElements() == 3
	})
	lrbid := &res.LRBID

	glb.Infof("total %d UTXO(s) in %s\n", len(walletOutputs), walletData.Account.String())

	sort.Slice(walletOutputs, func(i, j int) bool {
		return walletOutputs[i].Output.TokenBalance() > walletOutputs[j].Output.TokenBalance()
	})
	if len(walletOutputs) > maxNumberOfInputs {
		walletOutputs = slices.Clone(walletOutputs[:maxNumberOfInputs])
	}

	glb.PrintLRB(lrbid)
	if len(walletOutputs) <= 1 {
		glb.Infof("no need for compacting")
		os.Exit(0)
	}
	glb.Infof("%d ED25519 output(s) from account %s will be compacted into one", len(walletOutputs), walletData.Account.String())

	var prompt string
	glb.Assertf(feeAmount > 0, "tag-along fee is configured 0. Fee-less option not supported yet")

	prompt = fmt.Sprintf("compacting will cost %d of fees paid to the tag-along sequencer %s. Proceed?", feeAmount, tagAlongSeqID.StringShort())
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	tx, err := glb.GetClient().MakeCompactTransaction(walletData.PrivateKey, tagAlongSeqID, feeAmount, maxNumberOfInputs)
	if tx != nil {
		glb.Verbosef("------- the compacting transaction -------- \n%s\n--------------------------", tx.String())
	}
	glb.AssertNoError(err)
	glb.Assertf(tx != nil, "something wrong: transaction context is nil")
	txBytes := tx.Bytes()
	glb.Infof("Submitting compacting transaction with %d inputs (%d bytes)..", tx.NumInputs(), len(txBytes))
	err = glb.GetClient().SubmitTransaction(txBytes)
	glb.AssertNoError(err)

	if !glb.NoWait() {
		glb.TrackTxInclusion(tx.ID(), time.Second)
	}
}
