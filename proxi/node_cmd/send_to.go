package node_cmd

import (
	"encoding/hex"
	"os"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

// `proxi node send_to_wallet` and `proxi node send_to_chain` replace the
// deprecated `proxi node send -t <a/..|c/..>`: the target kind is fixed by
// the command, so a raw hex ID cannot be given with the wrong prefix, and
// each command checks the target against the ledger before building
// anything. The transfer itself is runSend (send.go), shared with `send`.

func initSendToWalletCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send_to_wallet <amount> <holder ID>",
		Short: "send tokens from the wallet to another wallet (sigLock holder)",
		Long: `Send <amount> tokens to the wallet identified by <holder ID>: 32 bytes hex,
without any prefix. The produced output is locked to that holder.

Before building the transaction the command checks whether the holder is
already known on the ledger, i.e. owns at least one output in the latest
reliable state. The node drops transactions signed by a holder it does not
know, so a brand-new wallet can spend only after this transfer has settled.
If the holder is unknown, the command warns and asks for confirmation, because
a mistyped ID is an unknown holder too and the tokens would be lost. With
--force the warning is printed and the transfer proceeds.
` + sendModesHelp,
		Args: cobra.ExactArgs(2),
		Run:  runSendToWalletCmd,
	}
	addSendFlags(cmd)
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runSendToWalletCmd(cmd *cobra.Command, args []string) {
	amount := parseAmountArg(args[0])

	holderIDBin, err := hex.DecodeString(args[1])
	glb.Assertf(err == nil && len(holderIDBin) == len(base.HolderID{}),
		"holder ID must be %d bytes hex without prefix, got '%s'", len(base.HolderID{}), args[1])
	var target ledger.SigLock
	copy(target[:], holderIDBin)
	glb.Infof("target: wallet %s", target.String())

	known, err := glb.GetClient().IsKnownController(target.ControllerID())
	glb.AssertNoError(err)
	if !known {
		glb.Infof("WARNING: holder %s is not known on the ledger: it owns no outputs in the latest reliable state.", args[1])
		glb.Infof("If the holder ID is mistyped, the tokens are lost. If it is a new wallet, it can spend only after this transfer settles.")
		if !glb.BypassYesNoPrompt() && !glb.YesNoPrompt("send to the unknown holder anyway?", false) {
			glb.Infof("exit")
			os.Exit(0)
		}
	}
	runSend(cmd, amount, target)
}

func initSendToChainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send_to_chain <amount> <chain ID>",
		Short: "send tokens from the wallet to a chain (chainLock)",
		Long: `Send <amount> tokens to the chain identified by <chain ID>: 24 bytes hex,
without any prefix. The produced output is locked under the standard
chainLock, spendable by whoever controls the chain.

The chain must exist in the latest reliable state; otherwise the transfer is
refused, since tokens locked to a chain nobody controls cannot be recovered.
` + sendModesHelp,
		Args: cobra.ExactArgs(2),
		Run:  runSendToChainCmd,
	}
	addSendFlags(cmd)
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runSendToChainCmd(cmd *cobra.Command, args []string) {
	amount := parseAmountArg(args[0])

	chainID, err := base.ChainIDFromHexString(args[1])
	glb.Assertf(err == nil, "chain ID must be %d bytes hex without prefix, got '%s': %v", base.ChainIDLength, args[1], err)

	_, _, err = glb.GetClient().GetChainOutputData(chainID)
	glb.Assertf(err != multistate.ErrNotFound,
		"chain %s is not known on the ledger: transfer refused", chainID.String())
	glb.AssertNoError(err)
	glb.Infof("target: chain %s", chainID.String())

	runSend(cmd, amount, ledger.ChainLockFromChainID(chainID))
}
