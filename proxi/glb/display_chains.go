package glb

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func LinesDelegationOutputs(outs []ledger.DelegationOutput, currentSlot uint32, verbosityLevel int, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	lst := slices.Clone(outs)
	sort.Slice(lst, func(i, j int) bool {
		return lst[i].Output.TokenBalance() > lst[j].Output.TokenBalance()
	})
	for _, o := range lst {
		status := ""
		if o.IsInFrozenSlot(currentSlot) {
			unfreeze := o.UnfreezeSlot()
			untilUnfreeze := time.Until(ledger.ClockTime(base.NewLedgerTime(base.Slot(unfreeze), 0)))
			h := untilUnfreeze / time.Hour
			hs := ""
			if h > 0 {
				hs = fmt.Sprintf("%d hours, ", h)
			}
			m := (untilUnfreeze - h*time.Hour) / time.Minute
			status = fmt.Sprintf("frozen until slot %d (%s%d min from now)", unfreeze, hs, m)
		} else if o.IsInSafeRevocationWindow(currentSlot) {
			_, to, applicable := o.SafeRevocationWindow()
			Assertf(applicable, "inconsistency: SafeRevocationWindow")
			untilEnd := time.Until(ledger.ClockTime(base.NewLedgerTime(base.Slot(to+1), 0)))
			m := untilEnd / time.Minute
			status = fmt.Sprintf("safe revocation until slot %d (for %d min)", to, m)
		} else if o.IsMarkedOnHold() {
			status = "on hold"
		} else if o.IsUnlockableByMaster(currentSlot) {
			status = "can be unlocked by master"
		}
		ln.Add("%34s  %s  %s", o.ChainID.String(), util.Th(o.Output.TokenBalance()), status)
		if VerbosityLevel() > 0 {
			ln.Add("     delegation target %s", util.Ref(o.Target.ChainID()).String())
			if o.IsMarkedFrozen() {
				ln.Add("     origin slot: %d, max frozen epochs: %d", o.OriginSlot, o.MaxFrozenEpochs)
				inflation := o.Output.TokenBalance() - o.OriginAmount
				unfreeze := o.UnfreezeSlot()
				totalSlots := unfreeze - uint32(o.OriginSlot) + 1
				perYear := inflation * uint64(ledger.Const.SlotsPerYear()) / uint64(totalSlots)
				rate := (float64(perYear) * 100) / float64(o.OriginAmount)
				lessShareForSafeRevocation := 1 - float64(ledger.Const.SafeRevocationSlots)/float64(uint32(o.MaxFrozenEpochs)*ledger.Const.DelegationEpochSlots+ledger.Const.SafeRevocationSlots)
				ln.Add("     estimated annualized inflation rate: %.2f%%", rate*lessShareForSafeRevocation)
			}
		}
	}
	return ln
}
