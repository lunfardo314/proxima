package node_cmd

import (
	"time"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var chainCmdDecompilePolicy bool

func initChainCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:   "chain <chainID, hex-encoded>",
		Short: `displays details of the specific chain`,
		Args:  cobra.ExactArgs(1),
		Run:   runChainCmd,
	}
	glb.AddFlagTarget(getBalanceCmd)
	getBalanceCmd.PersistentFlags().BoolVarP(&chainCmdDecompilePolicy, "decompile", "D", false,
		"if the chain is a foundry, also print its policy script (if any) in decompiled EasyFL source form")
	err := viper.BindPFlag("decompile", getBalanceCmd.PersistentFlags().Lookup("decompile"))
	glb.AssertNoError(err)
	getBalanceCmd.InitDefaultHelpCmd()
	return getBalanceCmd
}

func runChainCmd(_ *cobra.Command, args []string) {
	chainID, err := base.ChainIDFromHexString(args[0])
	glb.AssertNoError(err)

	lib := glb.GetTxLibrary()
	consts := glb.GetLedgerConstants()

	out, lrbid, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	glb.PrintLRB(&lrbid)

	// Parse the chain constraint (always present on a chain output).
	chainBin := out.Output.MustConstraintAt(ledger.ConstraintIndexChain)
	cc, err := lib.ParseChainConstraint(chainBin)
	glb.AssertNoError(err)

	// Delegation classification.
	dview, isDelegation, err := lib.ParseDelegationOutput(out.Output.Output, out.ID)
	glb.AssertNoError(err)

	// Sequencer classification: the output is a sequencer output iff it carries
	// the sequencer constraint at the fixed index. The OutputID's sequencer bit
	// reflects the producing transaction kind, not whether this particular output
	// is a sequencer chain output.
	hasSequencerConstraint := false
	if seqBytes, err := out.Output.ConstraintAt(ledger.SequencerConstraintFixedIndex); err == nil && len(seqBytes) > 0 {
		hasSequencerConstraint = true
	}

	// Foundry classification.
	isFoundry := false
	var foundry txbuildercore.FoundryView
	if fBytes, err := out.Output.ConstraintAt(ledger.ConstraintIndexFoundry); err == nil && len(fBytes) > 0 {
		if f, err := lib.ParseFoundryBytecode(fBytes); err == nil {
			isFoundry = true
			foundry = f
		}
	}

	glb.Infof("\nCHAIN OUTPUT DATA:\n-----------------")
	glb.Infof("chain ID:             %s", chainID.String())
	glb.Infof("output ID:            %s", out.ID.String())
	glb.Infof("token balance:        %s", util.Th(out.Output.TokenBalance()))
	glb.Infof("is delegation output: %v", isDelegation)
	if hasSequencerConstraint {
		glb.Infof("is sequencer output:  true (origin)")
	} else {
		glb.Infof("is sequencer output:  false")
	}
	glb.Infof("is foundry output:    %v", isFoundry)
	glb.Infof("is branch output:     %v", out.ID.IsBranchTransaction())
	glb.Infof("origin slot:          %d", cc.OriginSlot)
	glb.Infof("transition counter:   %d", cc.TransitionCounter)
	glb.Infof("cumulative inflation: %s (chain: %s, branch bonus: %s)",
		util.Th(cc.CumulativeChainInflation+cc.CumulativeBranchBonus),
		util.Th(cc.CumulativeChainInflation), util.Th(cc.CumulativeBranchBonus))
	if glb.IsVerbose() {
		// Per-index pretty form — wallet library for bytecode positions, structural
		// parse for amounts (index 0) and index-values (index 1).
		glb.Infof("constraints:")
		for j, raw := range out.Output.ConstraintsRawBytes() {
			if len(raw) == 0 {
				continue
			}
			glb.Infof("      [%d] %s", j, glb.FormatConstraintAtIndex(lib, byte(j), raw))
		}
	}
	glb.Infof("\n")

	if hasSequencerConstraint {
		if seqData, err := ledger.ParseSequencerData(out.Output); err == nil {
			glb.Infof("SEQUENCER DATA:\n-----------------")
			glb.Infof("%s", seqData.Lines("    ").String())
			glb.Infof("\n")
		}
	}

	if isDelegation {
		currentSlot := consts.LedgerTimeFromClockTime(time.Now()).Slot
		glb.Infof("DELEGATION OUTPUT DATA (current slot is %d):\n-----------------", currentSlot)
		printDelegationViewLines(dview, currentSlot, consts)
		glb.Infof("\n")
	}

	// Surface the sequencer constraint when attached. Its presence
	// marks this chain as a sequencer chain (the only kind of chain
	// that can be a delegation target); its two immutable args define
	// the cadence on which it admits delegations.
	if seqBytes, err := out.Output.ConstraintAt(ledger.SequencerConstraintFixedIndex); err == nil && len(seqBytes) > 0 {
		if seqView, seqErr := lib.ParseSequencerConstraint(seqBytes); seqErr == nil {
			glb.Infof("SEQUENCER CHAIN PARAMS:\n-----------------")
			glb.Infof("epoch slots:          %d", seqView.EpochSlots)
			glb.Infof("max frozen epochs:    %d", seqView.MaxFrozenEpochs)
			glb.Infof("\n")
		}
	}

	if isFoundry {
		printFoundryDetails(out, foundry, chainID, lib)
	}
}

// printDelegationViewLines is the wallet-side summary of a delegation
// chain output. Replaces the singleton-bound LinesDelegationData
// dump. Uses Constants for epoch math and ClockTime.
func printDelegationViewLines(view *txbuildercore.DelegationOutputView, currentSlot uint32, consts *txbuildercore.Constants) {
	glb.Infof("    master:                 %s", view.MasterID.String())
	glb.Infof("    target:                 %s", view.Target.String())
	glb.Infof("    maxFrozenEpochs:        %d", view.MaxFrozenEpochs)
	glb.Infof("    requiredInflationShare: %d promille (%.1f%%)",
		view.RequiredInflationShare, float64(view.RequiredInflationShare)/10)
	glb.Infof("    status:                 %s", glb.DelegationStatusString(view, currentSlot, consts))
	if view.IsMarkedFrozen() {
		_, lastSlot := consts.EpochLimits(view.Target, view.LastFrozenEpoch, view.EpochSlots)
		frozenSlots := int(lastSlot) - int(currentSlot) + 1
		glb.Infof("    frozen until epoch:     %d (last slot %d, %d slots from now)",
			view.LastFrozenEpoch, lastSlot, frozenSlots)
		if view.IsInSafeRevocationWindow(currentSlot, consts) {
			fromSRW, toSRW, _ := view.SafeRevocationWindow(consts)
			minutes := int(time.Until(consts.ClockTime(base.T(toSRW+1, 0))).Minutes())
			glb.Infof("    safe revocation window: slots %d - %d (%d min more)",
				fromSRW, toSRW, minutes)
		}
	}
}

// printFoundryDetails renders the FOUNDRY DATA section for `proxi node
// chain <chainID>` when the resolved output is a foundry. Shows tag
// (= the chain ID), circulating supply, the optional policy script at
// index 5 with a recognised description, and (when -D / --decompile is
// set) the decompiled EasyFL source of the policy.
func printFoundryDetails(out *ledger.OutputWithChainID, f txbuildercore.FoundryView, chainID base.ChainID, lib *txbuildercore.Library[any]) {
	glb.Infof("FOUNDRY DATA:\n-----------------")
	// The foundry's tag IS its chain ID — read from the chain
	// constraint, not from a foundry arg.
	glb.Infof("foundry tag:          %s", chainID.String())
	glb.Infof("circulating supply:   %s", util.Th(f.Supply))

	var policy []byte
	if p, err := out.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy); err == nil {
		policy = p
	}
	glb.Infof("policy:               %s", policyDescriptionLine(policy, lib))
	if len(policy) > 0 {
		glb.Infof("policy bytes:         %d", len(policy))
		if chainCmdDecompilePolicy {
			printDecompiledPolicySource(policy, lib, "    ")
		}
	}
	glb.Infof("\n")
}
