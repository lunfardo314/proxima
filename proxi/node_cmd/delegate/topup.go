package delegate

import (
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
	"github.com/spf13/cobra"
)

// `topup` adds tokens to one of the wallet's delegations. One the master can
// consume now is topped up and re-delegated by `delegate chain --add`, for the
// tag-along fee. A frozen one is topped up through its target with a top-up
// request (kb/delegation_topup.md): a tag-along to the target carrying the
// amount, which the target adds in place, freeze and share unchanged. The
// consolidator applies the same rule unattended; a person at a terminal has
// already decided, so this command reports the situation and does what it is
// told.
func initDelegationTopUpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topup <amount>",
		Short: `adds tokens from the wallet to an existing delegation, frozen or not`,
		Args:  cobra.ExactArgs(1),
		Run:   runDelegationTopUpCmd,
	}
	cmd.PersistentFlags().String("delegation", "", "delegation to top up (default: the smallest one)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

// topUpCandidate is one of the wallet's delegations, classified for display.
type topUpCandidate struct {
	view    *txbuildercore.DelegationOutputView
	oid     base.OutputID
	balance uint64
}

func runDelegationTopUpCmd(cmd *cobra.Command, args []string) {
	amountInt, err := strconv.Atoi(args[0])
	glb.AssertNoError(err)
	glb.Assertf(amountInt > 0, "amount must be > 0")
	amount := uint64(amountInt)

	walletData := glb.GetWalletData()
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	clnt := glb.GetClient()
	slot := glb.GetLedgerTimeNow().Slot

	res, err := clnt.GetOutputsForControllerID(walletData.Account.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeDelegateMaster,
		Chained:    client.ChainedOnly(),
		MaxOutputs: api.GetOutputsIterationCap,
	})
	glb.AssertNoError(err)
	glb.PrintLRB(&res.LRBID)

	all := make([]*topUpCandidate, 0, len(res.Outputs))
	for _, o := range res.Outputs {
		view, ok, err := lib.ParseDelegationOutput(o.Output.Output, o.ID)
		if err != nil || !ok {
			continue
		}
		all = append(all, &topUpCandidate{view: view, oid: o.ID, balance: o.Output.TokenBalance()})
	}
	glb.Assertf(len(all) > 0, "no delegation controlled by %s", walletData.Account.String())

	var chosen *topUpCandidate
	if s, _ := cmd.Flags().GetString("delegation"); s != "" {
		id, err := base.ChainIDFromHexString(s)
		glb.AssertNoError(err)
		for _, c := range all {
			if c.view.ChainID == id {
				chosen = c
			}
		}
		glb.Assertf(chosen != nil, "delegation %s is not controlled by this wallet", id.StringShort())
	} else {
		// smallest first, so balances even out across delegations
		sort.Slice(all, func(i, j int) bool { return all[i].balance < all[j].balance })
		chosen = all[0]
	}
	glb.Infof("topping up %s (%s, %s)", chosen.view.ChainID.StringShort(), util.Th(chosen.balance), accessLabel(chosen, slot, consts))
	if !chosen.view.IsInFrozenSlot(slot, consts) {
		delegateChainWithAdd(chosen.view.ChainID, amount)
		return
	}
	requestTopUp(chosen, amount)
}

// accessLabel says who can spend the delegation in the current slot.
func accessLabel(d *topUpCandidate, slot uint32, c *txbuildercore.Constants) string {
	switch {
	case d.view.IsMarkedOnHold():
		return "on hold"
	case d.view.IsInFrozenSlot(slot, c):
		return fmt.Sprintf("frozen until slot %d", d.view.UnfreezeSlot(c))
	case d.view.IsMarkedFrozen():
		return "inside its safe revocation window"
	default:
		return "not frozen"
	}
}

// requestTopUp sends the top-up request for a frozen delegation: the wallet's
// outputs are consumed, the request carries the amount, the rest returns to
// the wallet. The target has the tag-along window to take it; after that the
// request is the wallet's again and `proxi node compact` or the consolidator
// reclaims it, and after the reclaim window anybody may, so the outcome is
// worth watching.
func requestTopUp(d *topUpCandidate, amount uint64) {
	walletData := glb.GetWalletData()
	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()
	clnt := glb.GetClient()
	walletHolderID := base.HolderIDFromED25519PrivateKey(walletData.PrivateKey)

	// what the target will check before taking the request
	seqOut, _, err := clnt.GetChainOutput(d.view.Target)
	glb.AssertNoError(err)
	target := api.SequencerCandidate(d.view.Target, &seqOut.OutputWithID)
	glb.Assertf(amount >= target.MinimumTopUp, "sequencer %s takes top-ups of at least %s, %s requested",
		target.ID.StringShort(), util.Th(target.MinimumTopUp), util.Th(amount))
	glb.Assertf(d.view.AdvanceShare <= target.ShareLeft,
		"the delegation's pinned share %d is above what sequencer %s now leaves (%d): the request would be refused",
		d.view.AdvanceShare, target.ID.StringShort(), target.ShareLeft)

	walletOutputs, _, amountInWallet, err := clnt.GetTransferableOutputs(walletData.Account, 255)
	glb.AssertNoError(err)
	glb.Assertf(amountInWallet >= amount, "wallet holds %s, less than the %s to add", util.Th(amountInWallet), util.Th(amount))

	txb := txbuildercore.New(0)
	consumedBytes := make([][]byte, 0, len(walletOutputs))
	ts := askStopTimestamp()
	sumIn := uint64(0)
	for i, in := range walletOutputs {
		b := in.Output.Bytes()
		txb.ConsumeOutput(b, in.ID)
		consumedBytes = append(consumedBytes, b)
		ts = base.MaximumTime(ts, in.Timestamp())
		if i == 0 {
			txb.PutSignatureUnlock(0)
		} else {
			glb.AssertNoError(txb.PutUnlockReference(byte(i), ledger.ConstraintIndexLock, 0))
		}
		sumIn += in.Output.TokenBalance()
		if sumIn >= amount {
			break
		}
	}
	extra, err := lib.NewEnsureTopUpDelegationConstraint(d.view.ChainID)
	glb.AssertNoError(err)
	reqOut, err := lib.NewSequencerRequestOutput(amount, d.view.Target, walletHolderID, txbuilder_seq.RequestCodeTopUpDelegation, nil, extra)
	glb.AssertNoError(err)
	txb.ProduceOutput(reqOut.Bytes())
	if sumIn > amount {
		remainderOut, err := txbuildercore.NewSigLockOutput(lib, sumIn-amount, walletHolderID)
		glb.AssertNoError(err)
		txb.ProduceOutput(remainderOut.Bytes())
	}
	unfreeze := d.view.UnfreezeSlot(consts)
	glb.Infof("the target adds %s in place and prepays the advance on it; the freeze runs on until slot %d, share %d promille",
		util.Th(amount), unfreeze, d.view.AdvanceShare)
	glb.Infof("the sequencer has %d slots to take the request; if it does not, reclaim it with `proxi node compact` within the next %d slots",
		consts.TagAlongSlots, consts.TagAlongReclaimSlots-consts.TagAlongSlots)
	if !glb.YesNoPrompt(fmt.Sprintf("ask sequencer %s to add %s to delegation %s?", target.ID.StringShort(), util.Th(amount), d.view.ChainID.StringShort()), true) {
		glb.Infof("exit")
		os.Exit(0)
	}
	// Stamp + sign AFTER the prompt so the timestamp reflects the moment of
	// submission rather than the moment we offered the prompt.
	ts = base.MaximumTime(ts, askStopTimestamp())
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

// delegateChainWithAdd runs the `delegate chain` builder with --add set, so
// there is exactly one place that composes a master-side delegation transition.
func delegateChainWithAdd(chainID base.ChainID, amount uint64) {
	addAmount = amount
	runDelegationSubmitCmd(nil, []string{chainID.StringHex()})
}
