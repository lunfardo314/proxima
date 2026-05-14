package node_cmd

import (
	"bytes"
	"sort"

	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	sortBySafeRevocation bool
	decompilePolicy      bool
)

func initBalanceCmd() *cobra.Command {
	getBalanceCmd := &cobra.Command{
		Use:     "balance",
		Aliases: []string{"bal"},
		Short:   `displays account totals`,
		Args:    cobra.NoArgs,
		Run:     runBalanceCmd,
	}
	glb.AddFlagTarget(getBalanceCmd)
	getBalanceCmd.InitDefaultHelpCmd()

	getBalanceCmd.PersistentFlags().BoolVarP(&sortBySafeRevocation, "rw", "w", false, "sort by safe revocation window")
	err := viper.BindPFlag("rw", getBalanceCmd.PersistentFlags().Lookup("rw"))
	glb.AssertNoError(err)

	getBalanceCmd.PersistentFlags().BoolVarP(&decompilePolicy, "decompile", "D", false,
		"also print each foundry policy script in decompiled EasyFL source form")
	err = viper.BindPFlag("decompile", getBalanceCmd.PersistentFlags().Lookup("decompile"))
	glb.AssertNoError(err)

	return getBalanceCmd
}

func runBalanceCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	accountable := glb.MustGetTarget()

	res, err := glb.GetClient().GetOutputs(accountable.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		MaxOutputs: api.GetOutputsIterationCap,
	})
	glb.AssertNoError(err)
	if res.LimitExceeded {
		glb.Infof("WARNING: server-side iteration cap of %d hit; results are partial", api.GetOutputsIterationCap)
	}
	glb.PrintLRB(&res.LRBID)
	displayBalanceTotals(res.Outputs, accountable)
}

func displayBalanceTotals(outs []*ledger.OutputWithID, walletAccount ledger.Controller) {
	var sumOnNonDelegationChains, sumOutsideChains, sumDelegation uint64
	var numNonChains int

	delegations := make([]ledger.DelegationOutput, 0)
	otherChains := make([]ledger.OutputWithChainID, 0)

	for _, o := range outs {
		if oChain, err := o.AsChainOutput(); err == nil {
			if dOut, ok := ledger.AsDelegationOutput(o.Output, o.ID); ok {
				if !ledger.EqualControllers(ledger.SigLock(dOut.MasterID), walletAccount) {
					// for delegation locks only count those which are owned by the wallet
					continue
				}
				sumDelegation += o.Output.TokenBalance()
				delegations = append(delegations, dOut)
			} else {
				sumOnNonDelegationChains += o.Output.TokenBalance()
				otherChains = append(otherChains, *oChain)
			}
		} else {
			numNonChains++
			sumOutsideChains += o.Output.TokenBalance()
		}
	}
	currentSlot := ledger.TimeNow().Slot
	glb.Infof("Current slot is %d", currentSlot)
	glb.Infof("\nSUMMARY controlled by %s:", walletAccount.String())
	glb.Infof("    on %2d non-chain outputs:            %s", numNonChains, util.Th(sumOutsideChains))
	glb.Infof("    on %2d delegation outputs:           %s", len(delegations), util.Th(sumDelegation))
	glb.Infof("    on %2d non-delegation chain outputs: %s", len(otherChains), util.Th(sumOnNonDelegationChains))
	glb.Infof("-----------------\nTOTAL controlled on %d outputs: %s",
		len(delegations)+len(otherChains)+numNonChains, util.Th(sumDelegation+sumOnNonDelegationChains+sumOutsideChains))

	if len(delegations) == 0 {
		glb.Infof("\nNO DELEGATIONS")
	} else {
		if sortBySafeRevocation {
			sort.Slice(delegations, func(i, j int) bool {
				return delegations[i].UnfreezeSlot() < delegations[j].UnfreezeSlot()
			})
		} else {
			sort.Slice(delegations, func(i, j int) bool {
				return delegations[i].Output.TokenBalance() > delegations[j].Output.TokenBalance()
			})
		}
		glb.Infof("\nDELEGATIONS (%d):\n\n%s\n", len(delegations), glb.LinesDelegationOutputs(delegations, currentSlot, sumOutsideChains, "  ").String())
	}
	if len(otherChains) > 0 {
		glb.Infof("\nNON-DELEGATION CHAINS (%d):\n\n%s\n", len(otherChains), glb.LinesChainOutputs(otherChains, currentSlot, "  ").String())
	}

	displayNativeTokens(outs)
	displayFoundries(outs)
}

// foundrySummary describes one foundry output controlled by the wallet.
type foundrySummary struct {
	chainID  base.ChainID
	supply   uint64
	policy   []byte // bytecode at ConstraintIndexFoundryPolicy, nil if absent
	outputID base.OutputID
}

// displayNativeTokens scans every output for tokenAmount(tag, amount)
// constraints and reports the per-tag sum.
func displayNativeTokens(outs []*ledger.OutputWithID) {
	totals := make(map[base.ChainID]uint64)
	utxoCount := make(map[base.ChainID]int)
	for _, o := range outs {
		for _, raw := range o.Output.ConstraintsRawBytes() {
			ta, err := ledger.TokenAmountFromBytes(raw)
			if err != nil {
				continue
			}
			if totals[ta.Tag]+ta.Amount < totals[ta.Tag] {
				// overflow — should never happen in practice; cap.
				totals[ta.Tag] = ^uint64(0)
			} else {
				totals[ta.Tag] += ta.Amount
			}
			utxoCount[ta.Tag]++
		}
	}
	if len(totals) == 0 {
		glb.Infof("\nNO NATIVE TOKENS")
		return
	}
	tags := make([]base.ChainID, 0, len(totals))
	for t := range totals {
		tags = append(tags, t)
	}
	sort.Slice(tags, func(i, j int) bool {
		// largest balance first; tiebreak by tag bytes for stable output.
		if totals[tags[i]] != totals[tags[j]] {
			return totals[tags[i]] > totals[tags[j]]
		}
		return bytes.Compare(tags[i][:], tags[j][:]) < 0
	})
	glb.Infof("\nNATIVE TOKEN BALANCES (%d tag(s)):", len(tags))
	for _, t := range tags {
		glb.Infof("    %s  balance %s  on %d UTXO(s)",
			t.String(), util.Th(totals[t]), utxoCount[t])
	}
}

// displayFoundries finds every output carrying a foundry(...) constraint
// at ConstraintIndexFoundry and reports its chain ID, current supply,
// and whether an immutable policy script sits at index 5.
func displayFoundries(outs []*ledger.OutputWithID) {
	var foundries []foundrySummary
	for _, o := range outs {
		fBytes, err := o.Output.ConstraintAt(ledger.ConstraintIndexFoundry)
		if err != nil {
			continue
		}
		f, err := ledger.FoundryFromBytes(fBytes)
		if err != nil {
			continue
		}
		cc := o.Output.ChainConstraint()
		if cc == nil {
			continue
		}
		// At origin the chain ID is still NilChainID; the real chain ID
		// is derivable from the output ID.
		chainID := cc.ChainID
		if chainID == base.NilChainID {
			chainID = base.MakeOriginChainID(o.ID)
		}
		var policy []byte
		if p, err := o.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy); err == nil {
			policy = p
		}
		foundries = append(foundries, foundrySummary{
			chainID:  chainID,
			supply:   f.Supply,
			policy:   policy,
			outputID: o.ID,
		})
	}
	if len(foundries) == 0 {
		glb.Infof("\nNO FOUNDRIES")
		return
	}
	sort.Slice(foundries, func(i, j int) bool {
		// largest supply first; tiebreak by chain ID for stable output.
		if foundries[i].supply != foundries[j].supply {
			return foundries[i].supply > foundries[j].supply
		}
		return bytes.Compare(foundries[i].chainID[:], foundries[j].chainID[:]) < 0
	})
	glb.Infof("\nFOUNDRIES (%d):", len(foundries))
	for _, f := range foundries {
		glb.Infof("    %s  supply %s  policy: %s  (out %s)",
			f.chainID.String(), util.Th(f.supply), policyDescriptionLine(f.policy), f.outputID.StringShort())
		if decompilePolicy && len(f.policy) > 0 {
			printDecompiledPolicySource(f.policy, "        ")
		}
	}
}

// policyDescriptionLine returns a short human-readable label for the
// foundry policy bytes at index 5 of a foundry output. Recognises the
// two predefined policies; falls back to a "custom (...)" description
// for anything else. Returns "no policy" for empty/absent bytecode.
func policyDescriptionLine(policy []byte) string {
	if len(policy) == 0 {
		return "no policy"
	}
	if bytes.Equal(policy, ledger.FoundryNonDestructibleBytecode()) {
		return ledger.FoundryNonDestructibleName
	}
	// Try foundryMaxSupply($0) — bytecode is parametric so we can't
	// compare bytes directly; identify by name via the library and
	// print the cap if it's parseable.
	return describePolicy(policy)
}

// printDecompiledPolicySource decompiles the policy bytecode and prints
// it as a single indented line. Used by --decompile / -D flags on
// `balance` and `chain`.
func printDecompiledPolicySource(policy []byte, indent string) {
	lib := ledger.L(base.MaxSlot)
	src, err := lib.DecompileBytecode(policy)
	if err != nil {
		glb.Infof("%ssource: <decompile failed: %v>", indent, err)
		return
	}
	glb.Infof("%ssource: %s", indent, src)
}

// describePolicy returns a short human-readable label for an unknown
// policy bytecode, attempting to recognise the foundryMaxSupply(N)
// case by parsing the first-level call.
func describePolicy(policy []byte) string {
	lib := ledger.L(base.MaxSlot)
	sym, _, args, err := lib.ParseBytecodeOneLevel(policy)
	if err != nil {
		return "custom (unparseable)"
	}
	switch sym {
	case "foundryMaxSupply":
		if len(args) == 1 {
			// The inline literal arg is z-encoded uint -- decode best-effort.
			return "foundryMaxSupply(" + uintFromInlineLiteralStr(args[0]) + ")"
		}
		return "foundryMaxSupply(?)"
	case ledger.FoundryNonDestructibleName:
		return ledger.FoundryNonDestructibleName
	default:
		return "custom (" + sym + ")"
	}
}

// uintFromInlineLiteralStr decodes a z-encoded uint64 inline-literal
// argument into a thousand-separated decimal string. Falls back to a
// hex dump on parse failure.
func uintFromInlineLiteralStr(arg []byte) string {
	raw := arg
	// strip the easyfl inline-data prefix if present
	if len(raw) > 0 && raw[0] == 0x80 {
		raw = raw[1:]
	}
	var n uint64
	for _, b := range raw {
		n = (n << 8) | uint64(b)
	}
	return util.Th(n)
}
