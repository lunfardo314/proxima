package delegate

import (
	"bytes"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/sequencer/txbuilder_seq"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/smallkv"
	"github.com/spf13/cobra"
)

const (
	// askStopAllDefaultMax is how many delegations 'askstop all' stops when no cap is given;
	// askStopAllHardMax bounds the cap. Every request is one output of a single transaction.
	askStopAllDefaultMax = 50
	askStopAllHardMax    = 100
)

func initRevokeDelegationCmd() *cobra.Command {
	revokeCmd := &cobra.Command{
		Use:     "askstop <delegation ID> | all [max]",
		Aliases: util.List("stop"),
		Short:   "send 'stop delegation' request(s) to the target sequencer(s)",
		Long: fmt.Sprintf(`Sends 'stop delegation' requests to target sequencers, in one transaction.

  askstop <delegation ID>   stops the given delegation
  askstop all [max]         stops the wallet's frozen delegations closest to unfreezing,
                            up to max (default %d, at most %d). 'all 1' stops the one
                            that unfreezes first.

Only delegations frozen for more than a few slots are requested: an unfrozen one the
wallet can consume directly, and one unfreezing within a minute is better waited out.`,
			askStopAllDefaultMax, askStopAllHardMax),
		Args: cobra.RangeArgs(1, 2),
		Run:  runRevokeDelegationCmd,
	}

	glb.AddFlagTarget(revokeCmd)

	revokeCmd.InitDefaultHelpCmd()
	return revokeCmd
}

// askStopRequest is one 'stop delegation' request of the transaction being composed.
type askStopRequest struct {
	out      *ledger.OutputWithID
	view     *txbuildercore.DelegationOutputView
	unfreeze uint32
	// compensation is fee + allowance: the tag-along fee paid from the wallet plus what the
	// target may take out of the delegation itself (see glb.AskStopCost).
	compensation, fee, allowance uint64
}

func runRevokeDelegationCmd(_ *cobra.Command, args []string) {
	walletData := glb.GetWalletData()

	glb.Infof("wallet account is: %s", walletData.Account.String())

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)
	clnt := glb.GetClient()

	ts := askStopTimestamp()

	var requests []*askStopRequest
	if args[0] == "all" {
		maxRequests := askStopAllDefaultMax
		if len(args) == 2 {
			m, err := strconv.Atoi(args[1])
			glb.Assertf(err == nil && m >= 1 && m <= askStopAllHardMax, "max must be a number between 1 and %d", askStopAllHardMax)
			maxRequests = m
		}
		requests = collectFrozenDelegations(clnt, lib, consts, walletData.Account, walletHolderID, ts.Slot, maxRequests)
		glb.Assertf(len(requests) > 0, "no frozen delegations to stop")
	} else {
		glb.Assertf(len(args) == 1, "max is accepted only with 'all'")
		delegationID, err := base.ChainIDFromHexString(args[0])
		glb.AssertNoError(err)

		out, _, err := clnt.GetChainOutput(delegationID)
		glb.AssertNoError(err)
		view, ok, err := lib.ParseDelegationOutput(out.Output.Output, out.ID)
		glb.AssertNoError(err)
		glb.Assertf(ok, "not a delegation output: %s", delegationID.String())
		if glb.IsVerbose() {
			glb.Infof("delegation output:\n%s", out.String())
		}
		glb.Assertf(view.MasterID == walletHolderID, "this wallet is not a master controller of the delegation %s", delegationID.String())
		glb.Infof("delegation target ID: %s", view.Target.String())

		// `askstop` is meaningful only while the master CANNOT unlock the
		// delegation directly (i.e. it's in a frozen slot). Otherwise the
		// master can just consume the output.
		glb.Assertf(view.IsInFrozenSlot(ts.Slot, consts), "delegation is unlockable by master, no need for revocation")
		unfreeze := view.UnfreezeSlot(consts)
		glb.Assertf(unfreeze > ts.Slot+6, "delegation is not frozen or safe revocation window is very close, just wait up to a minute")
		requests = []*askStopRequest{{out: &out.OutputWithID, view: view, unfreeze: unfreeze}}
	}

	// The request output carries the ordinary tag-along fee; whatever
	// compensation it does not cover is authorised as an allowance and comes
	// out of the delegation itself. That is the point of the allowance: a
	// delegator need not park liquid tokens just to be able to stop. Shared
	// with the display path so the figure shown by `node chain` / `balance`
	// is the one actually charged. One eval request for all of them, and one
	// more for the ceilings: public nodes allow only a few eval calls per minute.
	costItems := make([]glb.AskStopCostItem, len(requests))
	ceilingSources := make([]string, len(requests))
	for i, r := range requests {
		costItems[i] = glb.AskStopCostItem{Target: r.view.Target, Balance: r.out.TokenBalance(), UnfreezeSlot: r.unfreeze, AdvanceShare: r.view.AdvanceShare}
		// Ceiling the constraint will enforce. Measured from the delegation
		// output's own slot, so it does not move while the request sits in the
		// tag-along window.
		ceilingSources[i] = glb.ChainInflationMultiStepSource(r.out.TokenBalance(), r.out.ID.Slot(), r.unfreeze-r.out.ID.Slot())
	}
	costs, err := glb.AskStopCosts(clnt, ts.Slot, costItems)
	glb.AssertNoError(err)
	ceilings, err := clnt.EvalU64s(0, ceilingSources)
	glb.AssertNoError(err)

	var totalFee, totalAllowance uint64
	for i, r := range requests {
		r.compensation, r.fee, r.allowance = costs[i].Total, costs[i].Fee, costs[i].Allowance
		glb.Assertf(r.compensation > 0, "estimated cost of stopping the delegation %s is 0", r.view.ChainID.StringShort())
		glb.Assertf(r.allowance <= ceilings[i], "computed allowance %s exceeds the ceiling %s", util.Th(r.allowance), util.Th(ceilings[i]))
		totalFee += r.fee
		totalAllowance += r.allowance

		glb.Infof("delegation %s -> %s, unfreezes in slot %d, balance %s",
			r.view.ChainID.StringShort(), r.view.Target.StringShort(), r.unfreeze, util.Th(r.out.TokenBalance()))
		glb.Infof("   estimated compensation to the sequencer: %s", util.Th(r.compensation))
		glb.Infof("   paid from this wallet (tag-along fee): %s", util.Th(r.fee))
		glb.Infof("   taken from the delegation (allowance): %s", util.Th(r.allowance))
	}

	// Pull wallet inputs (all sigLock-controlled outputs).
	walletOutputs, _, amountInWallet, err := clnt.GetTransferableOutputs(walletData.Account, 255)
	glb.AssertNoError(err)
	glb.Assertf(len(walletOutputs) > 0, "wallet has no outputs to create transaction")

	// The allowance covers the compensation, never the fee itself: the target
	// refuses a request paying under its declared minimum, so a wallet short of
	// the fee cannot buy its way in out of the delegation balance.
	glb.Assertf(amountInWallet >= totalFee,
		"wallet holds %s, less than the %s in tag-along fees required by the target sequencer(s) — fund the wallet before stopping",
		util.Th(amountInWallet), util.Th(totalFee))

	if len(requests) > 1 {
		glb.Infof("%d stop requests in one transaction: %s in tag-along fees from the wallet, %s in allowances from the delegations",
			len(requests), util.Th(totalFee), util.Th(totalAllowance))
	}
	if totalAllowance > 0 {
		var prompt string
		if len(requests) == 1 {
			prompt = fmt.Sprintf("authorise sequencer %s to take up to %s out of delegation %s?",
				requests[0].view.Target.StringShort(), util.Th(totalAllowance), requests[0].view.ChainID.StringShort())
		} else {
			prompt = fmt.Sprintf("authorise the target sequencers to take up to %s in total out of the %d delegations?",
				util.Th(totalAllowance), len(requests))
		}
		if !glb.YesNoPrompt(prompt, true) {
			glb.Infof("exit")
			os.Exit(0)
		}
	}

	txb := txbuildercore.New(0)
	consumedBytes := make([][]byte, 0, len(walletOutputs))
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			err := txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0)
			glb.AssertNoError(err)
		}
	}

	// Compose one ask-stop-delegation sequencer-request output per delegation.
	for _, r := range requests {
		extra, err := lib.NewEnsureStopDelegationConstraint(r.view.ChainID, r.allowance)
		glb.AssertNoError(err)
		params := smallkv.New()
		params.Set(txbuilder_seq.FieldRevokeDelegationID, r.view.ChainID[:])
		reqOut, err := lib.NewSequencerRequestOutput(
			r.fee,
			r.view.Target,
			walletHolderID,
			txbuilder_seq.RequestCodeAskStopDelegation,
			&params,
			extra,
		)
		glb.AssertNoError(err)
		txb.ProduceOutput(reqOut.Bytes())
	}

	// Remainder back to wallet.
	if amountInWallet > totalFee {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, amountInWallet-totalFee, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}

	var prompt string
	if len(requests) == 1 {
		prompt = fmt.Sprintf("send request to stop delegation %s to the sequencer %s?",
			requests[0].view.ChainID.StringShort(), requests[0].view.Target.String())
	} else {
		prompt = fmt.Sprintf("send requests to stop %d delegations?", len(requests))
	}
	if !glb.YesNoPrompt(prompt, true) {
		glb.Infof("exit")
		os.Exit(0)
	}

	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt; otherwise a
	// slow confirmation makes the tx "born stale".
	ts = askStopTimestamp()
	for _, in := range walletOutputs {
		ts = base.MaximumTime(ts, in.Timestamp())
	}
	txb.SetTimestamp(ts)
	txb.ComputeInputCommitment()
	txb.SignED25519(walletData.PrivateKey)

	txBytes := txb.Bytes()
	txid, err := txbuildercore.TxIDFromBytes(txBytes)
	glb.AssertNoError(err)

	if err := glb.SubmitAndDisplay(txBytes, consumedBytes...); err != nil {
		os.Exit(1)
	}

	glb.TrackTxInclusion(txid, time.Second)
}

func askStopTimestamp() base.LedgerTime {
	ts := glb.GetLedgerTimeNow()
	if ts.IsSlotBoundary() {
		ts = ts.AddTicks(5)
	}
	return ts
}

// collectFrozenDelegations lists the delegations the wallet controls as master, keeps those
// frozen beyond the near future, and returns up to maxRequests of them, the ones unfreezing
// first ahead. Delegations the master can already consume, or which unfreeze within a few
// slots, are reported and left alone, for the same reasons as in the single-ID path.
func collectFrozenDelegations(clnt *client.APIClient, lib *txbuildercore.Library[any], consts *txbuildercore.Constants,
	walletAccount ledger.SigLock, walletHolderID base.HolderID, slot uint32, maxRequests int) []*askStopRequest {

	res, err := clnt.GetOutputsForControllerID(walletAccount.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeDelegateMaster,
		Chained:    client.ChainedOnly(),
		MaxOutputs: api.GetOutputsIterationCap,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	ret := make([]*askStopRequest, 0, len(res.Outputs))
	skipped := 0
	for _, o := range res.Outputs {
		view, ok, err := lib.ParseDelegationOutput(o.Output.Output, o.ID)
		if err != nil || !ok || view.MasterID != walletHolderID {
			continue
		}
		unfreeze := view.UnfreezeSlot(consts)
		if !view.IsInFrozenSlot(slot, consts) || unfreeze <= slot+6 {
			skipped++
			continue
		}
		ret = append(ret, &askStopRequest{out: o, view: view, unfreeze: unfreeze})
	}
	sort.Slice(ret, func(i, j int) bool {
		if ret[i].unfreeze != ret[j].unfreeze {
			return ret[i].unfreeze < ret[j].unfreeze
		}
		return bytes.Compare(ret[i].view.ChainID[:], ret[j].view.ChainID[:]) < 0
	})
	glb.Infof("found %d delegation(s) controlled by %s: %d frozen, %d unlockable by master or unfreezing within a minute (skipped)",
		len(ret)+skipped, walletAccount.String(), len(ret), skipped)
	if len(ret) > maxRequests {
		glb.Infof("stopping the first %d to unfreeze", maxRequests)
		ret = ret[:maxRequests]
	}
	return ret
}
