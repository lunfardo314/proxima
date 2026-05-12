package chess_cmd

import (
	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initAcceptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accept <chainID> <uci-move>",
		Short: "join an existing chess game (black) — first half-move + ≥ 2× origin stake",
		Long: `Join an existing chess game as black. <chainID> is the chain ID the
opener (white) printed after ` + "`new`" + `. Black plays the first
half-move described by <uci-move> and tops up the chess UTXO to
≥ 2× the origin stake (default: exactly 2×; override via --new-amount).

` + uciMoveFormatHelp,
		Args: cobra.ExactArgs(2),
		Run:  runAcceptCmd,
	}
	cmd.Flags().Uint64("new-amount", 0, "new chess-UTXO amount (≥ 2× origin stake); 0 → exactly 2× origin")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runAcceptCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID := parseChainID(args[0])
	uci := args[1]
	newAmt, err := cmd.Flags().GetUint64("new-amount")
	glb.AssertNoError(err)

	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()
	clnt := glb.GetClient()

	gs, _ := fetchChessUTXO(chainID)
	glb.Assertf(len(gs.State.BlackHolder) == 0,
		"chess game already accepted (black holder is set)")

	originAmount := gs.Amount
	if newAmt == 0 {
		newAmt = 2 * originAmount
	}
	glb.Assertf(newAmt >= 2*originAmount,
		"--new-amount %d < 2 × origin stake %d", newAmt, originAmount)

	spec, err := chess_poc.UCIToMoveSpec(gs.State.Board, uci)
	glb.AssertNoError(err)
	boardAfter, err := chess_poc.ApplyMoveSpec(gs.State.Board, spec)
	glb.AssertNoError(err)

	// Funding: cover (newAmt - originAmount) for the chess-UTXO top-up
	// from black's wallet, plus a dedicated tag-along input.
	blackContribution := newAmt - originAmount
	chessFunding := pickFundingInputs(clnt, wallet.Account, blackContribution)
	tagAlongInput := pickFundingInput(clnt, wallet.Account, fee)
	for _, o := range chessFunding {
		if o.ID == tagAlongInput.ID {
			tagAlongInput = nil
			break
		}
	}
	if tagAlongInput == nil {
		outs, _, _, err := clnt.GetTransferableOutputs(wallet.Account)
		glb.AssertNoError(err)
	outer:
		for _, o := range outs {
			if o.Output.TokenBalance() < fee {
				continue
			}
			for _, c := range chessFunding {
				if c.ID == o.ID {
					continue outer
				}
			}
			tagAlongInput = o
			break
		}
		glb.Assertf(tagAlongInput != nil, "no distinct wallet UTXO available for tag-along")
	}

	// Resolve the chess UTXO again as OutputWithChainID for the builder.
	owc, _, err := clnt.GetChainOutput(chainID)
	glb.AssertNoError(err)

	ts := nextTxTimestamp(owc.ID.Timestamp())

	glb.Infof("accepting chess game %s", chainID.StringShort())
	glb.Infof("  origin stake: %d  →  new amount %d (black contributes %d)",
		originAmount, newAmt, blackContribution)
	glb.Infof("  first move:   %s", uci)
	glb.Infof("  tag-along to: %s (fee %d)", seqID.StringShort(), fee)

	txb, err := chess_poc.BuildAcceptance(chess_poc.BuildAcceptanceParams{
		BlackPrivKey:  wallet.PrivateKey,
		BlackSigLock:  wallet.Account,
		OriginUTXO:    owc,
		FundingInputs: chessFunding,
		NewAmount:     newAmt,
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   ts,
	})
	glb.AssertNoError(err)

	runChessAction("acceptance", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	if !glb.NoWait() {
		fetchAndPrintBoard(chainID, "after acceptance")
	}
}
