package foundry

import (
	"bytes"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

// Init returns the `proxi node foundry ...` subcommand tree. Future
// subcommands (mint / burn / retire) attach here.
func Init() *cobra.Command {
	foundryCmd := &cobra.Command{
		Use:   "foundry",
		Short: "subcommands for native-token foundries (claude/archive/shipped/native_token.md)",
		Args:  cobra.NoArgs,
	}
	foundryCmd.AddCommand(
		initFoundryCreateCmd(),
		initFoundryMintCmd(),
		initFoundryBurnCmd(),
		initFoundryRetireCmd(),
	)
	foundryCmd.InitDefaultHelpCmd()
	return foundryCmd
}

// assertWalletControlsFoundry stops early when the foundry output is not
// locked by this wallet's sigLock. Mint, burn and retire all unlock the
// foundry with the transaction signature, which nothing but that sigLock
// accepts; without this the node rejects the tx with an unlock error that
// says nothing about the cause. A delegated foundry is the case worth
// naming: its supply is still the delegation master's to move, but only
// over the master unlock path, which these commands do not build.
func assertWalletControlsFoundry(lib *txbuildercore.Library[any], out *ledger.Output, holderID base.HolderID) {
	probe, err := txbuildercore.NewSigLockOutput(lib, 1, holderID)
	glb.AssertNoError(err)
	wantLock, err := probe.ConstraintAt(txbuildercore.ConstraintIndexLock)
	glb.AssertNoError(err)

	gotLock, err := out.ConstraintAt(ledger.ConstraintIndexLock)
	glb.Assertf(err == nil && bytes.Equal(gotLock, wantLock),
		"the foundry is not locked by a sigLock (a delegated foundry looks like this): "+
			"revoke the delegation with `proxi node delegate askstop` before minting, burning or retiring")

	ivBin, err := out.ConstraintAt(ledger.ConstraintIndexIndexValues)
	glb.AssertNoError(err)
	values, err := txbuildercore.DecodeIndexValuesTuple(ivBin)
	glb.AssertNoError(err)
	glb.Assertf(len(values) > 0 && bytes.Equal(values[0], holderID[:]),
		"the foundry's sigLock belongs to another account, this wallet cannot unlock it")
}
