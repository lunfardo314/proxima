package chess_cmd

import (
	"time"

	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initWaitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wait <chainID>",
		Short: "poll the LRB once per second for chess-chain transitions; print each new state",
		Long: `wait polls the node's "get chain output" endpoint at one-second
intervals. Whenever the chess UTXO's OutputID changes (the opponent
has played, or the chain terminated), the new board + state are
printed, including the inclusion depth in the LRB.

Exits when the chain terminates (UTXO no longer in state) or after
the configured --timeout.`,
		Args: cobra.ExactArgs(1),
		Run:  runWaitCmd,
	}
	cmd.Flags().Duration("timeout", 0, "stop polling after this duration (0 = forever)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runWaitCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	chainID := parseChainID(args[0])
	timeout, err := cmd.Flags().GetDuration("timeout")
	glb.AssertNoError(err)

	clnt := glb.GetClient()
	start := time.Now()
	var lastOID base.OutputID
	first := true

	glb.Infof("waiting on chess chain %s (polling every %s)…", chainID.StringShort(), defaultPollEvery)

	for {
		if timeout > 0 && time.Since(start) > timeout {
			glb.Infof("wait timed out after %s", timeout)
			return
		}
		owc, lrb, err := clnt.GetChainOutput(chainID)
		if err != nil {
			// Chain might have terminated.
			glb.Infof("chain %s no longer in state (%v) — game terminated; exiting wait",
				chainID.StringShort(), err)
			return
		}
		if owc.ID == lastOID && !first {
			time.Sleep(defaultPollEvery)
			continue
		}
		gs, perr := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
		if perr != nil {
			glb.Infof("chain output present but doesn't parse as a chess game state: %v", perr)
			time.Sleep(defaultPollEvery)
			continue
		}

		banner := "new chess game state"
		if first {
			banner = "current chess game state (initial)"
		} else {
			banner = "NEW state — opponent moved (or game terminated)"
		}
		txid := owc.ID.TransactionID()
		glb.Infof("%s  (LRB %s, tx %s included)",
			banner, lrb.StringShort(), txid.StringShort())
		glb.Infof("%s", gs.Lines("    ").String())

		first = false
		lastOID = owc.ID
		time.Sleep(defaultPollEvery)
	}
}
