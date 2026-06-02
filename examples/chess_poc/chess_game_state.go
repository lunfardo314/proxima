package chess_poc

import (
	"bytes"
	"fmt"

	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// ChessGameState is a parsed, structured view of a chess covenant UTXO.
// It bundles the chain identity, the bounty, and the per-UTXO ChessState
// tuple at slot 4 — enough to render a human-readable picture of the
// game and to drive any covenant-side queries.
type ChessGameState struct {
	ChainID  base.ChainID  // chain ID of the game
	OutputID base.OutputID // UTXO ID
	Amount   uint64        // current bounty (token balance at slot 0)
	State    *ChessState   // 7-field state tuple at slot 4
}

// ParseAsChessGameOutput parses an output that is expected to be a chess
// covenant UTXO. Verifies that:
//   - the output has a chain constraint at slot 3 (and resolves origin
//     ChainID via blake2b(outputID) when needed);
//   - the lock at slot 2 byte-equals the chess() lock bytecode produced
//     by GetBins().LockBytecode (i.e. dispatches into chessGame);
//   - the chessState bytecode at slot 4 parses as a 7-tuple via
//     UnmarshalChessState.
//
// Returns a populated *ChessGameState on success. Any structural
// mismatch returns an error and a nil state.
func ParseAsChessGameOutput(o *ledger.OutputWithID) (*ChessGameState, error) {
	if o == nil || o.Output == nil {
		return nil, fmt.Errorf("ParseAsChessGameOutput: nil output")
	}
	cc, ok := ledger.ExtractChainData(o.Output, o.ID)
	if !ok {
		return nil, fmt.Errorf("ParseAsChessGameOutput: not a chain output")
	}

	bins := GetBins()
	lockBC, err := o.Output.ConstraintAt(ledger.ConstraintIndexLock)
	if err != nil {
		return nil, fmt.Errorf("ParseAsChessGameOutput: read lock bytecode: %w", err)
	}
	if !bytes.Equal(lockBC, bins.LockBytecode) {
		return nil, fmt.Errorf("ParseAsChessGameOutput: lock is not chess(); got %d bytes, want %d",
			len(lockBC), len(bins.LockBytecode))
	}

	stateBC, err := o.Output.ConstraintAt(ChessStateConstraintIndex)
	if err != nil {
		return nil, fmt.Errorf("ParseAsChessGameOutput: read chessState bytecode: %w", err)
	}
	state, err := UnmarshalChessState(stateBC)
	if err != nil {
		return nil, fmt.Errorf("ParseAsChessGameOutput: parse chessState: %w", err)
	}

	return &ChessGameState{
		ChainID:  cc.ChainID,
		OutputID: o.ID,
		Amount:   o.Output.TokenBalance(),
		State:    state,
	}, nil
}

// =============================================================================
// Pretty printing
// =============================================================================

// Lines renders a multi-line human-readable view of the game state:
// metadata (chain id, bounty, last move, deadline, flags, players)
// followed by an 8×8 board rendering with standard piece letters
// (uppercase = white, lowercase = black, '.' = empty).
func (g *ChessGameState) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	if g == nil {
		ret.Add("<nil ChessGameState>")
		return ret
	}
	oid := g.OutputID
	ret.Add("Chess game UTXO %s", oid.StringShort())
	ret.Add("  chainID:      %s", g.ChainID.String())
	ret.Add("  amount:       %s tokens", util.Th(g.Amount))
	ret.Add("  side-to-move: %s", sideName(g.State.SideToMove()))
	ret.Add("  last move:    %s", moveSpecPretty(g.State.LastMoveSpec))
	ret.Add("  deadline:     slot %d, tick %d", g.State.Deadline.Slot, g.State.Deadline.Tick)
	ret.Add("  flags:        %s", flagsPretty(g.State.Flags))
	ret.Add("  white holder: %s", g.State.WhiteHolder.String())
	if len(g.State.BlackHolder) == 32 {
		var bid base.HolderID
		copy(bid[:], g.State.BlackHolder)
		ret.Add("  black holder: %s", bid.String())
	} else {
		ret.Add("  black holder: <empty — pre-acceptance>")
	}
	ret.Add("")
	pref := ""
	if len(prefix) > 0 {
		pref = prefix[0]
	}
	for _, l := range renderBoard(g.State.Board, pref+"  ") {
		ret.Add("%s", l)
	}
	return ret
}

// =============================================================================
// Helpers
// =============================================================================

// renderBoard returns 9 strings: 8 board ranks (rank 7 first, rank 0 last)
// plus the "a b c d e f g h" file legend at the bottom. Each line is
// prefixed with `prefix`. Each square is one of P/N/B/R/Q/K (white,
// uppercase), p/n/b/r/q/k (black, lowercase), or '.' (empty).
func renderBoard(board []byte, prefix string) []string {
	if len(board) != 69 {
		return []string{prefix + "<invalid board>"}
	}
	out := make([]string, 0, 9)
	for rank := 7; rank >= 0; rank-- {
		row := fmt.Sprintf("%s%d  ", prefix, rank+1)
		for file := 0; file < 8; file++ {
			row += string(pieceLetter(board[rank*8+file]))
			if file < 7 {
				row += " "
			}
		}
		out = append(out, row)
	}
	out = append(out, prefix+"   a b c d e f g h")
	return out
}

// pieceLetter returns the standard chess letter for a piece byte
// (uppercase for white, lowercase for black). Returns '.' for empty
// and '?' for unrecognised bytes.
func pieceLetter(p byte) byte {
	switch p {
	case 0x00:
		return '.'
	case 0x11:
		return 'P'
	case 0x12:
		return 'N'
	case 0x13:
		return 'B'
	case 0x14:
		return 'R'
	case 0x15:
		return 'Q'
	case 0x16:
		return 'K'
	case 0x21:
		return 'p'
	case 0x22:
		return 'n'
	case 0x23:
		return 'b'
	case 0x24:
		return 'r'
	case 0x25:
		return 'q'
	case 0x26:
		return 'k'
	}
	return '?'
}

// sideName turns the chessValidator side byte (0x10 / 0x20) into "WHITE" /
// "BLACK". Anything else renders as a debug hex form.
func sideName(b byte) string {
	switch b {
	case SideWhite:
		return "WHITE"
	case SideBlack:
		return "BLACK"
	}
	return fmt.Sprintf("?(0x%02x)", b)
}

// squareName turns a 0..63 square index into algebraic notation (a1..h8).
func squareName(sq int) string {
	if sq < 0 || sq > 63 {
		return "??"
	}
	return fmt.Sprintf("%c%d", 'a'+byte(sq%8), 1+sq/8)
}

// moveSpecPretty renders a 5-byte chessValidator move spec as a short
// human form: "<from>-<to> <piece>[ capture][ castle][ ep][ =P]".
// Returns "<none>" for an empty or mis-sized spec (e.g. before move 1).
func moveSpecPretty(spec []byte) string {
	if len(spec) != 5 {
		return "<none>"
	}
	out := fmt.Sprintf("%s-%s %c", squareName(int(spec[0])), squareName(int(spec[1])), pieceLetter(spec[2]))
	flags := spec[3]
	if flags&0x01 != 0 {
		out += " capture"
	}
	if flags&0x08 != 0 {
		out += " castle"
	}
	if flags&0x10 != 0 {
		out += " ep"
	}
	if spec[4] != 0 {
		out += fmt.Sprintf(" =%c", pieceLetter(spec[4]))
	}
	return out
}

func flagsPretty(f byte) string {
	if f == 0 {
		return "(none)"
	}
	out := ""
	if f&FlagTieProposed != 0 {
		out += " tieProposed"
	}
	if f&^FlagTieProposed != 0 {
		out += fmt.Sprintf(" reserved=0x%02x", f&^FlagTieProposed)
	}
	return out
}
