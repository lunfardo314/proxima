package delegate

import (
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/exp/maps"
)

// TODO implement random delegation target option

var (
	targetChainIDStr string
	maxFreezeEpochs  uint8
	requiredCut      uint16
)

func initDelegateAmountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "amount <amount> [flags]",
		Short: `delegates amount to the target sequencer by creating delegation chain output`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegateAmountCmd,
	}

	glb.AddFlagTarget(cmd)

	cmd.PersistentFlags().StringVarP(&targetChainIDStr, "delegation_target", "q", "", "target sequencer id")
	err := viper.BindPFlag("delegation_target", cmd.PersistentFlags().Lookup("delegation_target"))
	glb.AssertNoError(err)

	// 0 means use the ledger constant constDelegationMaxFrozenEpochs (default maximum)
	cmd.PersistentFlags().Uint8VarP(&maxFreezeEpochs, "epochs", "e", 0, "max frozen epochs allowed by the delegator (0 = maximum)")
	err = viper.BindPFlag("epochs", cmd.PersistentFlags().Lookup("epochs"))
	glb.AssertNoError(err)

	cmd.PersistentFlags().Uint16Var(&requiredCut, "cut", 900, "required inflation cut in promille (0-1000)")
	err = viper.BindPFlag("cut", cmd.PersistentFlags().Lookup("cut"))
	glb.AssertNoError(err)

	cmd.InitDefaultHelpCmd()
	return cmd
}

func runDelegateAmountCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	if targetChainIDStr == "" {
		glb.Infof("selecting optimal/random target sequencer..")
		targetSeqID, err = chooseRandomSequencerForDelegation()
		glb.AssertNoError(err)
	} else {
		targetSeqID, err = base.ChainIDFromHexString(targetChainIDStr)
		glb.Assertf(err == nil, "failed parsing target chainID: %v", err)
	}

	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	amount := uint64(amountInt)

	glb.Assertf(requiredCut <= 1000, "required inflation cut must be 0-1000 promille")

	consts := glb.GetLedgerConstants()
	client := glb.GetClient()

	ti, err := client.GetSequencerTargetInfo(targetSeqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", targetSeqID.StringShort(), err)

	nowSlot := glb.GetLedgerTimeNow().Slot
	est := estimateDelegation(consts, client, ti, amount, maxFreezeEpochs, requiredCut, targetSeqID, nowSlot)
	effCut := confirmDelegationEstimate(est, amount, requiredCut, targetSeqID)

	tagAlongSeqID := glb.GetTagAlongSequencerID()
	glb.Assertf(tagAlongSeqID != nil, "tag-along sequencer not specified")
	feeAmount, err := glb.GetRequiredTagAlongFee(*tagAlongSeqID)
	if err != nil {
		glb.Infof("error getting tag-along fee: %s", err)
		return
	}
	glb.Verbosef("tag-along fee: %s", util.Th(feeAmount))

	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	// "Minimum inflatable" floor for the chosen amount — projected
	// inflation over ts.Slot+10_000 slots starting from slot 0,
	// computed server-side via /eval (no singleton on the wallet).
	inflMin, err := client.EvalU64(0,
		fmt.Sprintf("chainInflationMultiStep(u64/%d, u64/%d, u64/%d)",
			consts.MinimumInflatableAmount0, 0, ts.Slot+10000))
	glb.AssertNoError(err)
	minimumAmount := consts.MinimumInflatableAmount0 + inflMin
	glb.Assertf(amount >= minimumAmount, "amount is too small, must be at least %s", util.Th(minimumAmount))

	// Cap the delegator's chosen depth against the target sequencer
	// chain's own maxFrozenEpochs (carried by its sequencer constraint
	// — see SequencerConstraintFixedIndex), not the library-wide default.
	targetMaxFrozenEpochs := byte(ti.MaxFrozenEpochs)
	targetEpochSlots := ti.EpochDurationSlots
	glb.Assertf(maxFreezeEpochs <= targetMaxFrozenEpochs, "wrong value of max freeze epochs: %d > target's max %d", maxFreezeEpochs, targetMaxFrozenEpochs)

	needed := amount + feeAmount
	res, err := client.GetOutputsForControllerID(walletData.Account.ControllerID(), apiclient.GetOutputsParams{
		LockType:  api.GetOutputsLockTypeSigLock,
		Chained:   apiclient.NonChainedOnly(),
		SortBy:    api.GetOutputsSortByAmount,
		SortOrder: api.GetOutputsSortOrderDesc,
		ForAmount: needed,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)
	walletOutputs := res.Outputs
	glb.Assertf(res.AvailableAmount >= needed, "not enough tokens. Needed %s, got %s", util.Th(needed), util.Th(res.AvailableAmount))
	sumIn := uint64(0)
	for _, o := range walletOutputs {
		sumIn += o.Output.TokenBalance()
	}

	// Precompute the input timestamp floor (pure data, no clock).
	var maxInputTs base.LedgerTime
	for _, in := range walletOutputs {
		maxInputTs = base.MaximumTime(maxInputTs, in.Timestamp())
	}

	prompt := fmt.Sprintf("delegate amount %s to sequencer %s (cut %d promille, plus tag-along fee %s)?",
		util.Th(amount), targetSeqID.String(), effCut, util.Th(feeAmount))
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + build + sign AFTER the prompt so the timestamp reflects the
	// moment of submission. The delegation output embeds a chain origin whose
	// originSlot must equal the tx slot (chain.easyfl), so the output is
	// composed with the finalised slot here.
	ts = glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(10)
	}
	ts = base.MaximumTime(ts, maxInputTs)

	// Wasm-style build via txbuildercore + helpers.
	txLib := glb.GetTxLibrary()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	txb := txbuildercore.New(0)

	consumedBytes := make([][]byte, 0, len(walletOutputs))
	totalAmountConsumed := uint64(0)
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		totalAmountConsumed += in.Output.TokenBalance()
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	delegationOut, err := txLib.NewDelegationInitOutput(txbuildercore.DelegationInitOutputParams{
		Amount:                amount,
		MasterID:              walletHolderID,
		Target:                targetSeqID,
		MaxFrozenEpochs:       maxFreezeEpochs,
		RequiredInflationCut:  effCut,
		StartSlot:             ts.Slot,
		EpochSlots:            targetEpochSlots,
		TargetMaxFrozenEpochs: targetMaxFrozenEpochs,
	})
	glb.AssertNoError(err)
	delegationOutputIdx := txb.ProduceOutput(delegationOut.Bytes())

	tagAlongOut, err := txbuildercore.NewTagAlongOutput(txLib, feeAmount, *tagAlongSeqID, walletHolderID)
	glb.AssertNoError(err)
	txb.ProduceOutput(tagAlongOut.Bytes())

	totalProducedFixed := amount + feeAmount
	if totalAmountConsumed > totalProducedFixed {
		remainderOut, err := txbuildercore.NewSigLockOutput(txLib, totalAmountConsumed-totalProducedFixed, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	delegationOid, err := base.NewOutputID(txid, delegationOutputIdx)
	glb.AssertNoError(err)
	delegationID := base.MakeOriginChainID(delegationOid)
	glb.Infof("\ndelegation ID is %s", delegationID.String())

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}
	_ = client // declared earlier for the input fetch only
	glb.TrackTxInclusion(txid, 2*time.Second)
}

// select randomly inverse proportionally coverage
// using random roulette wheel selection
func chooseRandomSequencerForDelegation() (base.ChainID, error) {
	outs, _, err := glb.GetClient().GetAllSequencerOutputs()
	glb.AssertNoError(err)

	glb.Assertf(len(outs) > 0, "no sequencer outputs")

	if len(outs) == 1 {
		// return the single
		for ret := range outs {
			return ret, nil
		}
	}
	// select random proportionally to inverse coverage
	maxCov := uint64(0)
	for _, out := range outs {
		cov := out.Output.TokenBalance() + uint64(out.Output.FrozenCoverage(0))
		if maxCov < cov {
			maxCov = cov
		}
	}
	m := make(map[base.ChainID]uint64)
	// Wallet-side "now" — singleton-free (ledger.SlotNow() reaches the
	// ledger.L() singleton).
	currentSlot := glb.GetLedgerTimeNow().Slot
	for seqID, out := range outs {
		if out.ID.Slot()+6 >= currentSlot {
			// skip inactive sequencers
			m[seqID] = maxCov - (out.Output.TokenBalance() + uint64(out.Output.FrozenCoverage(0)))
		}
	}

	ordered := maps.Keys(m)
	sort.Slice(ordered, func(i, j int) bool {
		return m[ordered[i]] < m[ordered[j]]
	})

	sum := uint64(0)
	rnd := uint64(rand.Intn(int(maxCov)))

	for i, seqID := range ordered {
		if i < len(ordered)-1 {
			sum += m[ordered[i+1]]
		}
		if i == len(ordered)-1 || rnd < sum {
			return seqID, nil
		}
	}
	panic("inconsistency in chooseRandomSequencerForDelegation")
}
