package chess_cmd

import (
	chess_poc "github.com/lunfardo314/proxima/examples/chess_poc"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/proxi/glb"
	"github.com/spf13/cobra"
)

// =============================================================================
// resign
// =============================================================================

func initResignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resign <chainID>",
		Short: "resign (side-to-move only) — full bounty to opponent",
		Args:  cobra.ExactArgs(1),
		Run:   runResignCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runResignCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	chainID := parseChainID(args[0])
	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()

	owc, _, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	gs, err := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
	glb.AssertNoError(err)
	glb.Assertf(len(gs.State.BlackHolder) == 32, "cannot resign before acceptance — use timeout instead")

	// Verify wallet is side-to-move.
	signerHolder := chess_poc.HolderIDOf(wallet.PrivateKey)
	var expected [32]byte
	if gs.State.SideToMove() == chess_poc.SideWhite {
		expected = gs.State.WhiteHolder
	} else {
		copy(expected[:], gs.State.BlackHolder)
	}
	glb.Assertf(signerHolder == expected, "wallet is not side-to-move")

	opponentLock := derivedOpponentLock(gs)
	tagAlongInput := pickFundingInput(glb.GetClient(), wallet.Account, fee)

	ts := nextTxTimestamp(owc.ID.Timestamp())

	glb.Infof("resigning chess game %s — full %d bounty to opponent",
		chainID.StringShort(), gs.Amount)

	txb, err := chess_poc.BuildResign(chess_poc.BuildResignParams{
		ResignerPrivKey: wallet.PrivateKey,
		OpponentLock:    opponentLock,
		PrevUTXO:        owc,
		TxTimestamp:     ts,
	})
	glb.AssertNoError(err)

	runChessAction("resign", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	glb.Infof("game terminated by resignation")
}

// =============================================================================
// accept-tie
// =============================================================================

func initAcceptTieCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "accept-tie <chainID>",
		Short: "accept a pending tie offer (opposite of the proposer) — 50/50 split",
		Args:  cobra.ExactArgs(1),
		Run:   runAcceptTieCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runAcceptTieCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	chainID := parseChainID(args[0])
	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()

	owc, _, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	gs, err := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
	glb.AssertNoError(err)
	glb.Assertf(len(gs.State.BlackHolder) == 32, "tie-accept requires the game to be past acceptance")
	glb.Assertf(gs.State.Flags&chess_poc.FlagTieProposed != 0, "predecessor has no tieProposed flag set")

	// Acceptor must be side-to-move (= the proposer's opponent).
	signerHolder := chess_poc.HolderIDOf(wallet.PrivateKey)
	var expected [32]byte
	if gs.State.SideToMove() == chess_poc.SideWhite {
		expected = gs.State.WhiteHolder
	} else {
		copy(expected[:], gs.State.BlackHolder)
	}
	glb.Assertf(signerHolder == expected, "wallet is not the tie-accept side (= side-to-move)")

	whiteLock := sigLockFromHolderID(gs.State.WhiteHolder)
	blackLock := sigLockFromHolderID(toHolderID(gs.State.BlackHolder))
	tagAlongInput := pickFundingInput(glb.GetClient(), wallet.Account, fee)

	ts := nextTxTimestamp(owc.ID.Timestamp())

	whiteShare := (gs.Amount + 1) / 2
	blackShare := gs.Amount / 2

	glb.Infof("accepting tie on chess game %s — split %d / %d (white / black)",
		chainID.StringShort(), whiteShare, blackShare)

	txb, err := chess_poc.BuildTieAccept(chess_poc.BuildTieAcceptParams{
		OpponentPrivKey: wallet.PrivateKey,
		WhiteLock:       whiteLock,
		BlackLock:       blackLock,
		PrevUTXO:        owc,
		TxTimestamp:     ts,
	})
	glb.AssertNoError(err)

	runChessAction("tie-accept", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	glb.Infof("game terminated by tie acceptance")
}

// =============================================================================
// timeout
// =============================================================================

func initTimeoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "timeout <chainID>",
		Short: "claim a chess chain after the deadline (pre-acceptance: white reclaims; post: opposite of side-to-move)",
		Args:  cobra.ExactArgs(1),
		Run:   runTimeoutCmd,
	}
	cmd.InitDefaultHelpCmd()
	return cmd
}

func runTimeoutCmd(cmd *cobra.Command, args []string) {
	glb.InitLedgerFromNode()
	chainID := parseChainID(args[0])
	wallet := glb.GetWalletData()
	seqID, fee := tagAlongFee()

	owc, _, err := glb.GetClient().GetChainOutput(chainID)
	glb.AssertNoError(err)
	gs, err := chess_poc.ParseAsChessGameOutput(&owc.OutputWithID)
	glb.AssertNoError(err)

	signerHolder := chess_poc.HolderIDOf(wallet.PrivateKey)
	if len(gs.State.BlackHolder) == 0 {
		glb.Assertf(signerHolder == gs.State.WhiteHolder, "pre-acceptance timeout-claim must be signed by white")
	} else {
		var sideHolder [32]byte
		if gs.State.SideToMove() == chess_poc.SideWhite {
			sideHolder = gs.State.WhiteHolder
		} else {
			copy(sideHolder[:], gs.State.BlackHolder)
		}
		glb.Assertf(signerHolder != sideHolder, "post-acceptance timeout-claim must be signed by the opposite of side-to-move")
	}

	tagAlongInput := pickFundingInput(glb.GetClient(), wallet.Account, fee)

	// txTs strictly past the deadline. Use the configured deadline + 1.
	claimTs := base.T(gs.State.Deadline.Slot+1, 1)
	// If the LRB has already advanced past that, the API will still
	// accept (server checks txSlot ≥ deadline). The +1 keeps us off
	// the slot boundary.

	glb.Infof("claiming chess chain %s via timeout (deadline slot %d, claim slot %d) — bounty %d",
		chainID.StringShort(), gs.State.Deadline.Slot, claimTs.Slot, gs.Amount)

	txb, err := chess_poc.BuildTimeoutClaim(chess_poc.BuildTimeoutClaimParams{
		ClaimantPrivKey: wallet.PrivateKey,
		ClaimantLock:    wallet.Account,
		PrevUTXO:        owc,
		TxTimestamp:     claimTs,
	})
	glb.AssertNoError(err)

	runChessAction("timeout-claim", txb, wallet.PrivateKey, wallet.Account, seqID, fee, tagAlongInput)
	glb.Infof("game terminated by timeout-claim")
}

// =============================================================================
// helpers
// =============================================================================

func derivedOpponentLock(gs *chess_poc.ChessGameState) ledger.SigLock {
	var opponent base.HolderID
	if gs.State.SideToMove() == chess_poc.SideWhite {
		copy(opponent[:], gs.State.BlackHolder)
	} else {
		opponent = gs.State.WhiteHolder
	}
	return sigLockFromHolderID(opponent)
}

func toHolderID(b []byte) base.HolderID {
	var h base.HolderID
	copy(h[:], b)
	return h
}

// sigLockFromHolderID wraps a 32-byte holderID as a SigLock. ledger.SigLock
// is a type alias of base.HolderID, so this is just a re-cast.
func sigLockFromHolderID(h base.HolderID) ledger.SigLock {
	return ledger.SigLock(h)
}
