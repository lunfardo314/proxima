package node_cmd

import (
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/client"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/cobra"
)

func initGetOutputsCmd() *cobra.Command {
	getOutputsCmd := &cobra.Command{
		Use:     "utxo",
		Aliases: []string{"outputs", "utxo"},
		Short:   `returns all UTXOs (outputs) locked in the accountable from the heaviest state of the latest epoch`,
		Args:    cobra.NoArgs,
		Run:     runGetOutputsCmd,
	}

	getOutputsCmd.InitDefaultHelpCmd()
	return getOutputsCmd
}

func runGetOutputsCmd(_ *cobra.Command, _ []string) {
	accountable := glb.MustGetTarget()
	lib := glb.GetTxLibrary()

	res, err := glb.GetClient().GetOutputsForControllerID(accountable.ControllerID(), client.GetOutputsParams{
		LockType:   api.GetOutputsLockTypeAll,
		MaxOutputs: 100,
	})
	glb.AssertNoError(err)

	if len(res.Outputs) == 0 {
		glb.Infof("no outputs found")
		return
	}
	if res.LimitExceeded {
		glb.Infof("WARNING: server-side iteration cap hit; results are partial")
	}
	glb.PrintLRB(&res.LRBID)

	for i, o := range res.Outputs {
		glb.Infof("\n-- output %d --", i)
		glb.Infof("   id %s, hex = %s", o.ID.String(), o.ID.StringHex())
		// Lock kind: read the symbol of the lock bytecode at slot 2
		// via the wallet library's one-level parser. No singleton.
		lockBin, _ := o.Output.ConstraintAt(ledger.ConstraintIndexLock)
		lockSym, _, _, _ := lib.ParseBytecodeOneLevel(lockBin)
		glb.Infof("   amount: %s, lock name: '%s'", util.Th(o.Output.TokenBalance()), lockSym)
		// Chain ID via wallet-side parser (handles origin → blake2b(oid)).
		if chainBin, err := o.Output.ConstraintAt(ledger.ConstraintIndexChain); err == nil && len(chainBin) > 0 {
			if chainID, err := lib.ParseChainConstraintChainID(chainBin, o.ID); err == nil {
				glb.Verbosef("   chain id: %s", chainID.StringHex())
				// Native-token annotations: mark foundries (tag = chainID).
				if fBytes, err := o.Output.ConstraintAt(ledger.ConstraintIndexFoundry); err == nil && len(fBytes) > 0 {
					if f, err := lib.ParseFoundryBytecode(fBytes); err == nil {
						glb.Infof("   foundry: tag=%s supply=%s", chainID.String(), util.Th(f.Supply))
						if p, err := o.Output.ConstraintAt(ledger.ConstraintIndexFoundryPolicy); err == nil && len(p) > 0 {
							glb.Infof("      policy: %s", policyDescriptionLine(p, lib))
						}
					}
				}
			}
		}
		for _, raw := range o.Output.ConstraintsRawBytes() {
			if ta, err := lib.ParseTokenAmountBytecode(raw); err == nil {
				glb.Infof("   tokenAmount: tag=%s amount=%s", ta.Tag.String(), util.Th(ta.Amount))
			}
		}
		glb.Verbosef("   raw data: %s (%d bytes) ", o.Output.Hex(), len(o.Output.Bytes()))
		if glb.IsVerbose() {
			glb.Infof("   parsed constraints:")
			// Decompile every constraint slot via the wallet library.
			// Singleton-free; produces raw EasyFL source rather than the
			// typed pretty-form ConstraintFromBytesWithLib emits.
			for j, raw := range o.Output.ConstraintsRawBytes() {
				if len(raw) == 0 {
					continue
				}
				src, err := lib.DecompileBytecode(raw)
				if err != nil {
					glb.Infof("        [%d] <decompile error: %v>", j, err)
				} else {
					glb.Infof("        [%d] %s", j, src)
				}
			}
		}
	}
}
