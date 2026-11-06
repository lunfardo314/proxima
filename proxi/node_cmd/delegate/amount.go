package delegate

import (
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	apiclient "github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/consolidator"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var targetChainIDStr string

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

	addFlagCut(cmd)

	cmd.InitDefaultHelpCmd()
	return cmd
}

func runDelegateAmountCmd(cmd *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	var err error
	var targetSeqID base.ChainID

	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	amount := uint64(amountInt)

	requiredCut := delegatorCut(cmd)
	if targetChainIDStr == "" {
		targetSeqID, err = chooseRandomSequencerForDelegation(requiredCut, amount)
		glb.AssertNoError(err)
	} else {
		targetSeqID, err = base.ChainIDFromHexString(targetChainIDStr)
		glb.Assertf(err == nil, "failed parsing target chainID: %v", err)
	}

	consts := glb.GetLedgerConstants()
	client := glb.GetClient()

	ti, err := client.GetSequencerTargetInfo(targetSeqID)
	glb.Assertf(err == nil, "cannot retrieve target info for %s: %v", targetSeqID.StringShort(), err)
	assertRoomFor(targetSeqID, ti, amount)

	nowSlot := glb.GetLedgerTimeNow().Slot
	est := estimateDelegation(consts, client, ti, amount, byte(consts.DelegationMaxFrozenEpochs), requiredCut, targetSeqID, nowSlot)
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

	prompt := fmt.Sprintf("delegate amount %s to sequencer %s (delegator cut %d promille, plus tag-along fee %s)?",
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
		Amount:               amount,
		MasterID:             walletHolderID,
		Target:               targetSeqID,
		RequiredInflationCut: effCut,
		StartSlot:            ts.Slot,
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

	glb.SetTimestamp(txb, ts)
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

// chooseRandomSequencerForDelegation draws the target by the delegation
// rating (kb/sequencer_rating.md) among the sequencers active in the LRB
// state that leave a delegator at least requiredCut and have room for amount
// under their max_frozen bound.
func chooseRandomSequencerForDelegation(requiredCut uint16, amount uint64) (base.ChainID, error) {
	active, _, err := glb.FetchSequencerCandidates()
	if err != nil {
		return base.ChainID{}, err
	}
	eligible := txbuildercore.DelegationCandidates(active, requiredCut, amount)
	if len(eligible) == 0 {
		return base.ChainID{}, fmt.Errorf("none of the %d sequencers active in the last %d slots leaves delegators at least %d promille and has room for %s under its max_frozen bound",
			len(active), txbuildercore.ActiveSequencerSlots, max(requiredCut, 1), util.Th(amount))
	}
	drawn, err := consolidator.DrawDelegationTarget(eligible, nil, amount, rand.Intn, countRoomNow())
	if err != nil {
		return base.ChainID{}, err
	}
	glb.Infof("target sequencer drawn by rating among %d candidates: %s (leaves %d promille)",
		len(eligible), drawn.ID.StringShort(), drawn.ShareLeft)
	return drawn.ID, nil
}

// countRoomNow is the per-epoch count cap check of a target at the current slot.
func countRoomNow() func(c txbuildercore.SequencerCandidate) (bool, error) {
	return consolidator.FreezeCountRoom(glb.GetClient(), glb.GetTxLibrary(), glb.GetLedgerConstants(), glb.GetLedgerTimeNow().Slot)
}

// assertRoomFor refuses a delegation or top-up of amount to a sequencer that
// would not freeze it: its max_frozen bound leaves no room for the amount, or
// its max_frozen_delegations cap is 0 or reached in every epoch a freeze can
// run to.
func assertRoomFor(targetID base.ChainID, ti *api.SequencerTargetInfo, amount uint64) {
	c := txbuildercore.SequencerCandidate{ID: targetID, MaxFrozen: ti.MaxFrozen, MaxFrozenDelegations: ti.MaxFrozenDelegations}
	if len(ti.FrozenCoverage) > 0 && ti.FrozenCoverage[0] > 0 {
		c.FrozenCoverage = uint64(ti.FrozenCoverage[0])
	}
	glb.Assertf(c.HasRoomFor(amount), "sequencer %s does not take %s: its freeze caps leave no room (max_frozen %s, frozen now %s, max_frozen_delegations %s)",
		ti.SequencerID, util.Th(amount), capString(ti.MaxFrozen), util.Th(c.FrozenCoverage), capString(ti.MaxFrozenDelegations))
	ok, err := countRoomNow()(c)
	glb.AssertNoError(err)
	if !ok {
		glb.Assertf(false, "sequencer %s has every epoch it can freeze into at its max_frozen_delegations cap %d", ti.SequencerID, *ti.MaxFrozenDelegations)
	}
}

func capString[T uint32 | uint64](v *T) string {
	if v == nil {
		return "default"
	}
	return util.Th(*v)
}
