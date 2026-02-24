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
			ln.Add("     delegation target %s", o.Target.String())
			totalInflation := o.CumulativeChainInflation + o.CumulativeBranchBonus
			ln.Add("     origin slot: %d, transitions: %d, cumulative inflation: %s",
				o.OriginSlot, o.TransitionCounter, util.Th(totalInflation))
			if o.IsMarkedFrozen() {
				unfreeze := o.UnfreezeSlot()
				totalSlots := unfreeze - o.OriginSlot + 1
				lib := ledger.L(currentSlot)
				perYear := totalInflation * uint64(lib.SlotsPerYear()) / uint64(totalSlots)
				lessShareForSafeRevocation := 1 - float64(lib.SafeRevocationSlots)/float64(uint32(o.MaxFrozenEpochs)*lib.DelegationEpochSlots+lib.SafeRevocationSlots)
				ln.Add("     estimated annualized inflation: %s/year (adj. %.2f%%)",
					util.Th(uint64(float64(perYear)*lessShareForSafeRevocation)), lessShareForSafeRevocation*100)
			}
		}
	}
	return ln
}

func LinesChainOutputs(outs []ledger.OutputWithChainID, currentSlot uint32, prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)

	for _, o := range outs {
		ln.Add("%34s  %20s   since slot: %d, last active %d slots ago, transitions: %d",
			o.ChainID.String(), util.Th(o.Output.TokenBalance()), o.OriginSlot,
			currentSlot-uint32(o.ID.Slot()), o.TransitionCounter)
		if IsVerbose() {
			totalInflation := o.CumulativeChainInflation + o.CumulativeBranchBonus
			ln.Add("      cumulative inflation: %s", util.Th(totalInflation))
			ln.Add("        chain inflation:    %s", util.Th(o.CumulativeChainInflation))
			ln.Add("        branch bonus:       %s", util.Th(o.CumulativeBranchBonus))
		}
	}
	return ln
}
