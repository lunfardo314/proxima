package chess_cmd

import (
	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initMoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "move <chainID> <uci-move>",
		Short: "play an ordinary half-move (side-to-move only)",
		Long: `Play one half-move in an existing chess game. The wallet calling this
command must hold the side-to-move's key. --propose-tie sets the
tieProposed flag in the produced state — the opponent's next action
can either play a normal move (clears the flag) or take the
` + "`accept-tie`" + ` branch.

` + uciMoveFormatHelp,
		Args: cobra.ExactArgs(2),
		Run:  runMoveCmd,
	}
	cmd.Flags().Bool("propose-tie", false, "set the tieProposed flag in the produced state")
	cmd.Flags().Uint64("top-up", 0, "extra tokens to add to the chess UTXO bounty (0 → keep amount unchanged)")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runMoveCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	chainID := parseChainID(args[0])
	uci := args[1]
	proposeTie, err := cmd.Flags().GetBool("propose-tie")
	glb.AssertNoError(err)
	topUp, err := cmd.Flags().GetUint64("top-up")
	glb.AssertNoError(err)

	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()
	clnt := glb.GetClient()

	owc, _, err := clnt.GetChainOutput(chainID)
	glb.AssertNoError(err)
	gs, err := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
	glb.AssertNoError(err)
	glb.Assertf(len(gs.State.BlackHolder) == 32, "chess game not yet accepted (black holder empty)")

	// Verify wallet is side-to-move.
	signerHolder := chess_poc.HolderIDOf(wallet.PrivateKey)
	var expected [32]byte
	if gs.State.SideToMove() == chess_poc.SideWhite {
		expected = gs.State.WhiteHolder
	} else {
		copy(expected[:], gs.State.BlackHolder)
	}
	glb.Assertf(signerHolder == expected,
		"wallet holder %x is not side-to-move (%s)", signerHolder, sideToMoveName(gs.State.SideToMove()))

	spec, err := chess_poc.UCIToMoveSpec(gs.State.Board, uci)
	glb.AssertNoError(err)
	boardAfter, err := chess_poc.ApplyMoveSpec(gs.State.Board, spec)
	glb.AssertNoError(err)

	newAmt := gs.Amount + topUp

	// Funding: cover top-up (if any) + a separate tag-along input. If
	// top-up is 0 we still need at least one funding input to sign the
	// tx (chess_builder requires "at least one signed funding input").
	chessFunding := pickFundingInputs(clnt, wallet.Account, topUp+1) // +1 ensures we pick something
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

	ts := nextTxTimestamp(owc.ID.Timestamp())

	glb.Infof("playing %s on chess game %s (side-to-move: %s)",
		uci, chainID.StringShort(), sideToMoveName(gs.State.SideToMove()))
	glb.Infof("  amount:       %d  →  %d", gs.Amount, newAmt)
	glb.Infof("  propose tie:  %v", proposeTie)
	glb.Infof("  tag-along to: %s (fee %d)", seqID.StringShort(), fee)

	txb, err := chess_poc.BuildMove(chess_poc.BuildMoveParams{
		MoverPrivKey:  wallet.PrivateKey,
		MoverSigLock:  wallet.Account,
		PrevUTXO:      owc,
		NewAmount:     newAmt,
		FundingInputs: chessFunding,
		MoveSpec:      spec,
		BoardAfter:    boardAfter,
		ProposeTie:    proposeTie,
		TxTimestamp:   ts,
	})
	glb.AssertNoError(err)

	runChessAction("move", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	if !glb.NoWait() {
		fetchAndPrintBoard(chainID, "after move")
	}
}

func sideToMoveName(b byte) string {
	if b == chess_poc.SideWhite {
		return "WHITE"
	}
	return "BLACK"
}
