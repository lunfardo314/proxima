package chess_cmd

import (
	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

func initNewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "new <stake> <uci-move>",
		Short: "open a new chess game (white) — stakes <stake> tokens and plays the first half-move",
		Long: `Open a new chess game as white. Stakes <stake> tokens into the chess
chain-UTXO, plays the first half-move described by <uci-move>, and
prints the resulting chain ID — share it with your opponent so they
can ` + "`accept`" + ` it.

` + uciMoveFormatHelp,
		Args: cobra.ExactArgs(2),
		Run:  runNewCmd,
	}
	cmd.Flags().Uint32("tslots", defaultTSlots, "per-game move-time budget in slots; deadline = txSlot + tslots")
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runNewCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()

	stake := parseUint(args[0], "stake")
	uci := args[1]
	tslots, err := cmd.Flags().GetUint32("tslots")
	glb.AssertNoError(err)

	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()

	// Compute the first half-move spec + resulting board from the canonical
	// starting position.
	startBoard := append([]byte(nil), chess_poc.CanonicalStartBoard...)
	spec, err := chess_poc.UCIToMoveSpec(startBoard, uci)
	glb.AssertNoError(err)
	boardAfter, err := chess_poc.ApplyMoveSpec(startBoard, spec)
	glb.AssertNoError(err)

	// Need stake-worth of sigLock funding for the chess UTXO + one
	// dedicated input for the tag-along fee.
	chessFunding := pickFundingInputs(glb.GetClient(), wallet.Account, stake)
	tagAlongInput := pickFundingInput(glb.GetClient(), wallet.Account, fee)
	// Ensure the tag-along input isn't also in the chess-funding set.
	for _, o := range chessFunding {
		if o.ID == tagAlongInput.ID {
			glb.Verbosef("(tag-along input collides with chess funding; falling back to a different UTXO)")
			tagAlongInput = nil
			break
		}
	}
	if tagAlongInput == nil {
		// Try again, this time excluding the chess-funding set.
		outs, _, _, err := glb.GetClient().GetTransferableOutputs(wallet.Account)
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
		glb.Assertf(tagAlongInput != nil, "no distinct wallet UTXO available to fund the %d-token tag-along fee", fee)
	}

	// Pick a timestamp respecting transaction pace from the first chess-
	// funding input's timestamp.
	ts := nextTxTimestamp(chessFunding[0].ID.Timestamp())

	glb.Infof("opening new chess game")
	glb.Infof("  stake:        %d", stake)
	glb.Infof("  T_slots:      %d (deadline = slot %d, tick 0)", tslots, ts.Slot+tslots)
	glb.Infof("  first move:   %s", uci)
	glb.Infof("  tag-along to: %s (fee %d)", seqID.StringShort(), fee)

	txb, err := chess_poc.BuildOrigin(chess_poc.BuildOriginParams{
		WhitePrivKey:  wallet.PrivateKey,
		WhiteSigLock:  wallet.Account,
		FundingInputs: chessFunding,
		Stake:         stake,
		TSlots:        tslots,
		FirstMoveSpec: spec,
		BoardAfter:    boardAfter,
		TxTimestamp:   ts,
	})
	glb.AssertNoError(err)

	txid := runChessAction("origin", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	chainID := base.MakeOriginChainID(base.MustNewOutputID(txid, 0))
	glb.Infof("\n=========================================================")
	glb.Infof("new chess game chain ID = %s", chainID.String())
	glb.Infof("share this chain ID with the opponent so they can `proxi node chess accept` it")
	glb.Infof("=========================================================")
	if !glb.NoWait() {
		fetchAndPrintBoard(chainID, "current state")
	}
}
