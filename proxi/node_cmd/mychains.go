package node_cmd

import (
	"bytes"
	"fmt"
	"os"
	"sort"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var delegationOnly bool

func initChainsCmd() *cobra.Command {
	chainsCmd := &cobra.Command{
		Use:   "mychains",
		Short: `lists chains controlled by the wallet account`,
		Args:  cobra.NoArgs,
		Run:   runChainsCmd,
	}
	chainsCmd.InitDefaultHelpCmd()
	chainsCmd.PersistentFlags().BoolVarP(&delegationOnly, "delegation", "d", false, "list delegations only")
	err := viper.BindPFlag("delegation", chainsCmd.PersistentFlags().Lookup("delegation"))
	glb.AssertNoError(err)

	return chainsCmd
}

func runChainsCmd(_ *cobra.Command, _ []string) {
	glb.InitLedgerFromNode()
	wallet := glb.GetWalletData()

	outs, lrbid, err := glb.GetClient().GetChainedOutputs(wallet.Account)
	glb.AssertNoError(err)

	glb.PrintLRB(lrbid)
	if len(outs) == 0 {
		glb.Infof("no chains have been found controlled by %s", wallet.Account.String())
		os.Exit(0)
	}

	sort.Slice(outs, func(i, j int) bool {
		return outs[i].ID.Timestamp().After(outs[j].ID.Timestamp())
	})

	if delegationOnly {
		listDelegations(wallet.Account, outs)
	} else {
		listChainedOutputs(wallet.Account, outs)
	}
}

func listChainedOutputs(addr ledger.AddressED25519, outs []*ledger.OutputWithChainID) {
	glb.Infof("\nlist of %d chain(s) indexed in the account %s",
		len(outs), addr.String())
	for i, o := range outs {
		seq := "NO"
		if o.ID.IsSequencerTransaction() {
			if showDelegationsOnly {
				continue
			}
			seqData, err := ledger.ParseSequencerData(o.Output)
			if err != nil {
				glb.Infof("error parsing sequencer data %s / %s : '%v'", o.ID.StringShort(), o.ChainID.StringShort(), err)
			}
			seq = "YES"
			seq = fmt.Sprintf("%s (%d/%d)", seqData.Name(), seqData.ChainHeight(), seqData.BranchHeight())
		}
		dOut, isDelegation := ledger.AsDelegationOutput(o.Output, o.ID)
		if isDelegation {
			if showSequencersOnly {
				continue
			}
		}
		lock := o.Output.Lock()
		glb.Infof("\n%2d: %s -- %s, hex: %s, sequencer: "+seq, i, o.ChainID.String(), o.ID.StringShort(), o.ID.StringHex())
		glb.Infof("      balance     : %s", util.Th(o.Output.TokenBalance()))
		glb.Infof("      lock        : %s", lock.String())
		thisControls := ""
		if ledger.EqualAccountables(addr, lock.Master()) {
			thisControls = " <- wallet account controls"
		}
		if isDelegation {
			delegatedToThis := ""
			if ledger.EqualAccountables(addr, dOut.Target) {
				delegatedToThis = " <- is delegated to the wallet account"
			}
			glb.Infof("      master      : %s"+thisControls, dOut.Master().String())
			glb.Infof("      delegated to: %s"+delegatedToThis, dOut.Target.String())
		} else {
			glb.Infof("      master      : %s"+thisControls, lock.String())
		}
	}
}

func listDelegations(addr ledger.AddressED25519, outs []*ledger.OutputWithChainID) {
	sort.Slice(outs, func(i, j int) bool {
		return bytes.Compare(outs[i].ChainID[:], outs[j].ChainID[:]) < 0
	})

	total := uint64(0)
	glb.Infof("\nList of delegations in account %s\n", addr.String())
	nowis := ledger.TimeNow()
	for _, o := range outs {
		dOut, isDelegation := ledger.AsDelegationOutput(o.Output, o.ID)
		if !isDelegation {
			continue
		}
		if !ledger.EqualAccountables(addr, dOut.Master()) {
			continue
		}

		glb.Infof("%s   %s  \t\t-> %s", dOut.ChainID.String(), util.Th(o.Output.TokenBalance()), dOut.Target.String())

		earned := o.Output.TokenBalance() - dOut.OriginAmount
		slots := nowis.Slot - dOut.OriginSlot
		perSlot := earned / uint64(slots)
		annualExtrapolationEarnings := uint64(ledger.Const.SlotsPerYear()) * perSlot
		annualRate := 100 * float64(annualExtrapolationEarnings) / float64(dOut.OriginAmount)
		glb.Verbosef("        inflation +%s since slot %d (%d slots), avg %s per slot, start amount %s,"+
			" annual rate: ~%.02f%%, last active %d slots back\n        output id: %s\n        hex output id: %s",
			util.Th(earned), dOut.OriginSlot, slots, util.Th(perSlot),
			util.Th(dOut.OriginAmount), annualRate, nowis.Slot-o.ID.Slot(),
			o.ID.String(), o.ID.StringHex(),
		)

		total += o.Output.TokenBalance()
	}
	glb.Infof("\nTotal delegated in %d outputs: %s", len(outs), util.Th(total))
}
