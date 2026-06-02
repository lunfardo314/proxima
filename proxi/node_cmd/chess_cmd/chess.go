// Package chess_cmd implements `proxi node chess ...` — the wallet-side
// CLI for the chess covenant PoC (examples/chess_poc). It lets two
// players carry a complete game through the Proxima ledger using the
// chess() lock + chessGame redeemer, with each half-move waiting for
// LRB-depth-1 inclusion before printing the resulting board.
//
// Subcommands:
//
//   new <stake> <uci-move>           — white opens the chain (chooses T_slots
//                                       and plays the first half-move).
//   accept <chainID> <uci-move>      — black joins by the chain ID.
//   move <chainID> <uci-move>        — make a half-move (--propose-tie sets
//                                       the tieProposed flag).
//   accept-tie <chainID>             — accept a pending tie offer.
//   resign <chainID>                 — resign (side-to-move only).
//   timeout <chainID>                — claim the chain after the deadline
//                                       (pre-acceptance: white; post-acceptance:
//                                       opposite of side-to-move).
//   status <chainID>                 — print the current board + state.
//   wait <chainID>                   — poll the LRB once per second; print the
//                                       board after each new transition and
//                                       its inclusion status.
//
// Tag-along: every action tx pays a fee to a sequencer via
// chess_poc.AttachTagAlong. Fee + sequencer come from the proxi profile
// (`tag_along.fee`, `tag_along.sequencer_id`).
package chess_cmd

import (
	"github.com/spf13/cobra"
)

// uciMoveFormatHelp is appended to the Long description of the parent
// `chess` command and every subcommand that accepts a <uci-move> arg, so
// `proxi node chess --help` (and per-subcommand --help) explains the
// move syntax without forcing users to leave the terminal.
const uciMoveFormatHelp = `UCI MOVE FORMAT (<uci-move>)

  The <uci-move> argument is a Universal Chess Interface (UCI) move
  string: lowercase, no spaces, no piece-letter prefix.

  Form:    <from-square><to-square>[<promotion-piece>]
           - <from-square> / <to-square> are algebraic, a1..h8
             (file a..h, rank 1..8). Both 2 chars.
           - <promotion-piece> is one of q r b n (queen / rook /
             bishop / knight), present iff a pawn reaches its
             back rank. The mover's colour is applied automatically.

  Examples:
    e2e4    — white pawn double push from e2 to e4
    e7e5    — black pawn double push from e7 to e5
    g1f3    — white knight from g1 to f3
    e1g1    — white kingside castle (king from e1 to g1; the rook
              move is detected and applied automatically)
    e1c1    — white queenside castle
    e8g8    — black kingside castle
    exd5    — NOT a UCI form; use e4d5 instead (UCI is from-to,
              the capture is detected from the board)
    e5d6    — en-passant capture (a pawn diagonal move to an empty
              square matching the board's EP-target byte; flagged
              automatically)
    e7e8q   — pawn promotes to queen on e8
    a7a8n   — pawn underpromotes to knight on a8

  Flag bits (capture, castle, en-passant, promotion piece) are set
  automatically from the source/destination squares + the current
  board — you never write them by hand. Full move legality is
  enforced by the chessValidator at submit time; this CLI's parser
  only constructs the 5-byte spec.`

// Init wires the `chess` subcommand tree under `proxi node`.
func Init() *cobra.Command {
	chessCmd := &cobra.Command{
		Use:   "chess [<subcommand>]",
		Short: "wallet-side CLI for the chess covenant PoC",
		Long: `Wallet-side CLI for the chess covenant PoC. Two players carry a
complete game through the Proxima ledger using the chess() lock +
chessGame redeemer, with each half-move waiting for LRB-depth-1
inclusion before printing the resulting board.

` + uciMoveFormatHelp,
		Args: cobra.NoArgs,
	}
	chessCmd.InitDefaultHelpCmd()
	chessCmd.AddCommand(
		initNewCmd(),
		initAcceptCmd(),
		initMoveCmd(),
		initAcceptTieCmd(),
		initResignCmd(),
		initTimeoutCmd(),
		initStatusCmd(),
		initWaitCmd(),
	)
	return chessCmd
}
