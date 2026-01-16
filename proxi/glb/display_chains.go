package glb

import (
	"fmt"
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func DelegationStatusString(o ledger.DelegationOutput, currentSlot uint32) (ret string) {
	if o.IsInFrozenSlot(currentSlot) {
		unfreeze := o.UnfreezeSlot()
		untilUnfreeze := time.Until(ledger.ClockTime(base.T(unfreeze, 0)))
		h := untilUnfreeze / time.Hour
		hs := ""
		if h > 0 {
			hs = fmt.Sprintf("%d hours, ", h)
		}
		m := (untilUnfreeze - h*time.Hour) / time.Minute
		ret = fmt.Sprintf("frozen until slot %d (%s%d min from now)", unfreeze, hs, m)
	} else if o.IsInSafeRevocationWindow(currentSlot) {
		_, to, applicable := o.SafeRevocationWindow()
		Assertf(applicable, "inconsistency: SafeRevocationWindow")
		untilEnd := time.Until(ledger.ClockTime(base.T(uint32(to+1), 0)))
		m := untilEnd / time.Minute
		ret = fmt.Sprintf("safe revocation until slot %d (for %d min more)", to, m)
	} else if o.IsMarkedOnHold() {
		ret = "on hold"
	} else if o.IsUnlockableByMaster(currentSlot) {
		ret = "can be unlocked by master"
	}
	return
}

func LinesDelegationOutputs(outs []ledger.DelegationOutput, currentSlot uint32, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	for _, o := range outs {
		status := DelegationStatusString(o, currentSlot)
		ln.Add("%34s  %20s  %s maxFrozen: %d", o.ChainID.String(), util.Th(o.Output.TokenBalance()), status, o.MaxFrozenEpochs)
		if VerbosityLevel() > 0 {
			ln.Add("     delegation target %s", util.Ref(o.Target.ChainID()).String())
			if o.IsMarkedFrozen() {
				ln.Add("     origin slot: %d, max frozen epochs: %d", o.OriginSlot, o.MaxFrozenEpochs)
				inflation := o.Output.TokenBalance() - o.OriginAmount
				unfreeze := o.UnfreezeSlot()
				totalSlots := unfreeze - o.OriginSlot + 1
				// Use currentSlot for timing constants
				lib := ledger.L(currentSlot)
				perYear := inflation * uint64(lib.SlotsPerYear()) / uint64(totalSlots)
				rate := (float64(perYear) * 100) / float64(o.OriginAmount)
				lessShareForSafeRevocation := 1 - float64(lib.SafeRevocationSlots)/float64(uint32(o.MaxFrozenEpochs)*lib.DelegationEpochSlots+lib.SafeRevocationSlots)
				ln.Add("     estimated annualized inflation rate: %.2f%%", rate*lessShareForSafeRevocation)
			}
		}
	}
	return ln
}

func LinesChainOutputs(outs []ledger.OutputWithChainID, currentSlot uint32, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)

	lib := ledger.L(currentSlot)
	for _, o := range outs {
		slots := currentSlot - uint32(o.OriginSlot)
		inflation := o.Output.TokenBalance() - o.OriginAmount
		yearly := uint64(lib.SlotsPerYear()) * inflation / uint64(slots)
		yearlyRate := 100 * float64(yearly) / float64(o.OriginAmount)
		ln.Add("%34s  %20s   since slot: %d, last active %d slots ago",
			o.ChainID.String(), util.Th(o.Output.TokenBalance()), o.OriginSlot, currentSlot-uint32(o.ID.Slot()))
		if IsVerbose() {
			ln.Add("      origin amount:        %s", util.Th(o.OriginAmount))
			ln.Add("      inflation:            %s", util.Th(o.Output.TokenBalance()-o.OriginAmount))
			ln.Add("      annualized inflation: %.2f%%", yearlyRate)
		}
	}
	return ln
}
