package chess_poc

import (
	"fmt"
	"strings"
)

// Piece bytes (chessValidator encoding: color<<4 | type). Mirrors the
// constants from chess_script.md §1 / chess_script.easyfl.
const (
	PieceEmpty byte = 0x00

	// White
	PieceWP byte = 0x11
	PieceWN byte = 0x12
	PieceWB byte = 0x13
	PieceWR byte = 0x14
	PieceWQ byte = 0x15
	PieceWK byte = 0x16

	// Black
	PieceBP byte = 0x21
	PieceBN byte = 0x22
	PieceBB byte = 0x23
	PieceBR byte = 0x24
	PieceBQ byte = 0x25
	PieceBK byte = 0x26
)

// Move-spec flag bits.
const (
	MoveFlagCapture byte = 0x01
	MoveFlagCheck   byte = 0x02 // recorded, not validated
	MoveFlagMate    byte = 0x04 // recorded, not validated
	MoveFlagCastle  byte = 0x08
	MoveFlagEP      byte = 0x10
)

// EPNone is the sentinel value for board[67] when no EP target is set.
const EPNone byte = 0xff

// Square helpers (0..63: rank*8 + file). a1=0, h8=63.

func pieceType(p byte) byte  { return p & 0x0f }
func pieceColor(p byte) byte { return p & 0xf0 }
func rankOf(sq int) int      { return sq / 8 }
func fileOf(sq int) int      { return sq % 8 }
func absInt(x int) int       { if x < 0 { return -x }; return x }

// crMask mirrors the chess_script.easyfl crMask function: clears
// castling-rights bits when a move touches one of the 6 special squares
// (king or rook home squares). Used to AND start[66] with the from-
// and to-square masks on every move.
func crMask(sq int) byte {
	switch sq {
	case 4:
		return 0xfc // e1: clears WK + WQ
	case 7:
		return 0xfe // h1: clears WK
	case 0:
		return 0xfd // a1: clears WQ
	case 60:
		return 0xf3 // e8: clears BK + BQ
	case 63:
		return 0xfb // h8: clears BK
	case 56:
		return 0xf7 // a8: clears BQ
	}
	return 0xff
}

// =============================================================================
// ApplyMoveSpec — generic move application
// =============================================================================

// ApplyMoveSpec applies a 5-byte chessValidator move spec to a 69-byte
// board and returns the result. Handles all special moves:
//
//   - captures (destination has opposite-color piece): just overwrite,
//     no extra bookkeeping.
//   - castling (flag bit 0x08): also move the matching rook.
//   - en passant (flag bit 0x10): also clear the captured pawn one
//     rank below/above `to`.
//   - promotion (spec[4] != 0): place the promotion piece on `to`
//     instead of the moving pawn.
//
// Plus the standard updates: king-pos bytes (64, 65), castling rights
// byte (66, AND'd with crMask(from) & crMask(to)), EP target byte (67,
// midpoint for a 2-square pawn push else 0xff), side-to-move byte (68,
// flipped).
//
// Errors only on malformed input (wrong sizes); no chess-legality
// checks — that's chessValidator's job, which runs inside the covenant
// at submit time.
func ApplyMoveSpec(start, spec []byte) ([]byte, error) {
	if len(start) != 69 {
		return nil, fmt.Errorf("ApplyMoveSpec: board must be 69 bytes, got %d", len(start))
	}
	if len(spec) != 5 {
		return nil, fmt.Errorf("ApplyMoveSpec: spec must be 5 bytes, got %d", len(spec))
	}
	from, to, piece, flags, promote := int(spec[0]), int(spec[1]), spec[2], spec[3], spec[4]
	if from < 0 || from > 63 || to < 0 || to > 63 {
		return nil, fmt.Errorf("ApplyMoveSpec: from/to out of range")
	}

	out := make([]byte, 69)
	copy(out, start)

	// Effective piece on `to`: promotion piece for promoting pawn, else
	// the moving piece itself.
	effective := piece
	if pieceType(piece) == 0x01 && promote != 0 {
		effective = promote
	}
	out[from] = PieceEmpty
	out[to] = effective

	// Castling: also move the rook.
	if flags&MoveFlagCastle != 0 {
		switch {
		case from == 4 && to == 6: // white kingside e1→g1, rook h1→f1
			out[7] = PieceEmpty
			out[5] = PieceWR
		case from == 4 && to == 2: // white queenside e1→c1, rook a1→d1
			out[0] = PieceEmpty
			out[3] = PieceWR
		case from == 60 && to == 62: // black kingside e8→g8, rook h8→f8
			out[63] = PieceEmpty
			out[61] = PieceBR
		case from == 60 && to == 58: // black queenside e8→c8, rook a8→d8
			out[56] = PieceEmpty
			out[59] = PieceBR
		}
	}

	// En passant: clear the captured pawn (one rank below for white, above for black).
	if flags&MoveFlagEP != 0 && pieceType(piece) == 0x01 {
		var capturedSq int
		if pieceColor(piece) == 0x10 {
			capturedSq = to - 8
		} else {
			capturedSq = to + 8
		}
		if capturedSq >= 0 && capturedSq < 64 {
			out[capturedSq] = PieceEmpty
		}
	}

	// King-pos bytes follow the king.
	switch piece {
	case PieceWK:
		out[64] = byte(to)
	case PieceBK:
		out[65] = byte(to)
	}

	// Castling rights: clear bits for any move touching e1/h1/a1/e8/h8/a8.
	out[66] = start[66] & crMask(from) & crMask(to)

	// EP target: midpoint of a 2-square pawn push, else "none".
	if pieceType(piece) == 0x01 && absInt(rankOf(to)-rankOf(from)) == 2 {
		out[67] = byte((from + to) / 2)
	} else {
		out[67] = EPNone
	}

	// Side flip.
	if start[68] == SideWhite {
		out[68] = SideBlack
	} else {
		out[68] = SideWhite
	}
	return out, nil
}

// =============================================================================
// UCI → moveSpec
// =============================================================================

// UCIToMoveSpec converts a UCI move string to a 5-byte chessValidator
// move spec, given the current board (needed to detect captures,
// en-passant, and castling from the source/destination squares).
//
// Supported UCI forms:
//   - 4 chars (e.g. "e2e4"): non-promoting move.
//   - 5 chars (e.g. "e7e8q"): promotion; last char is the piece letter
//     (q/r/b/n; uppercase or lowercase both accepted).
//
// Flag bits are set automatically:
//   - capture: destination has any non-empty piece of opposite color.
//   - castle:  king moves 2 files horizontally (e1g1, e1c1, e8g8, e8c8).
//   - en passant: pawn moves one file sideways onto an empty square AND
//     board[67] equals the destination square.
//
// Errors on syntactic problems and "obvious" semantic problems
// (empty source square, color mismatch, missing promotion piece on a
// back-rank pawn move, etc.). Full move-legality validation is the
// chessValidator's job at submit time.
func UCIToMoveSpec(board []byte, uci string) ([]byte, error) {
	if len(board) != 69 {
		return nil, fmt.Errorf("UCIToMoveSpec: board must be 69 bytes, got %d", len(board))
	}
	uci = strings.ToLower(strings.TrimSpace(uci))
	if len(uci) != 4 && len(uci) != 5 {
		return nil, fmt.Errorf("UCIToMoveSpec: expected 4 or 5 chars, got %q", uci)
	}
	from, err := parseUCISquare(uci[0:2])
	if err != nil {
		return nil, fmt.Errorf("UCIToMoveSpec: from: %w", err)
	}
	to, err := parseUCISquare(uci[2:4])
	if err != nil {
		return nil, fmt.Errorf("UCIToMoveSpec: to: %w", err)
	}

	piece := board[from]
	if piece == PieceEmpty {
		return nil, fmt.Errorf("UCIToMoveSpec: source square %s is empty", uci[0:2])
	}

	var flags, promote byte
	mover := pieceColor(piece)

	// Capture? Destination occupied by opposite-color piece.
	if board[to] != PieceEmpty {
		if pieceColor(board[to]) == mover {
			return nil, fmt.Errorf("UCIToMoveSpec: destination %s holds own piece", uci[2:4])
		}
		flags |= MoveFlagCapture
	}

	// En passant? Pawn move one file sideways onto an empty square whose
	// index matches board[67].
	if pieceType(piece) == 0x01 && board[to] == PieceEmpty && fileOf(from) != fileOf(to) {
		if board[67] != EPNone && int(board[67]) == to {
			flags |= MoveFlagCapture | MoveFlagEP
		} else {
			return nil, fmt.Errorf("UCIToMoveSpec: pawn diagonal to empty non-EP square")
		}
	}

	// Castling? King moves 2 files horizontally.
	if pieceType(piece) == 0x06 && absInt(fileOf(to)-fileOf(from)) == 2 {
		flags |= MoveFlagCastle
	}

	// Promotion? Pawn reaches the back rank.
	if pieceType(piece) == 0x01 {
		targetRank := -1
		if mover == 0x10 && rankOf(to) == 7 {
			targetRank = 7
		} else if mover == 0x20 && rankOf(to) == 0 {
			targetRank = 0
		}
		if targetRank >= 0 {
			if len(uci) != 5 {
				return nil, fmt.Errorf("UCIToMoveSpec: pawn promotion requires a piece letter (e.g. e7e8q)")
			}
			promote, err = promotionPieceFromUCI(mover, uci[4])
			if err != nil {
				return nil, fmt.Errorf("UCIToMoveSpec: %w", err)
			}
		}
	}
	if promote == 0 && len(uci) == 5 {
		return nil, fmt.Errorf("UCIToMoveSpec: trailing promotion char %q on non-promoting move", string(uci[4]))
	}

	return []byte{byte(from), byte(to), piece, flags, promote}, nil
}

// parseUCISquare turns "a1".."h8" into a 0..63 square index.
func parseUCISquare(s string) (int, error) {
	if len(s) != 2 {
		return -1, fmt.Errorf("square must be 2 chars, got %q", s)
	}
	f := int(s[0] - 'a')
	r := int(s[1] - '1')
	if f < 0 || f > 7 || r < 0 || r > 7 {
		return -1, fmt.Errorf("square %q out of range a1..h8", s)
	}
	return r*8 + f, nil
}

// promotionPieceFromUCI maps q/r/b/n + mover color → promotion piece byte.
// Pawn / king are rejected as invalid promotion targets.
func promotionPieceFromUCI(mover byte, c byte) (byte, error) {
	var t byte
	switch c {
	case 'q':
		t = 0x05
	case 'r':
		t = 0x04
	case 'b':
		t = 0x03
	case 'n':
		t = 0x02
	default:
		return 0, fmt.Errorf("invalid promotion piece %q (want q/r/b/n)", string(c))
	}
	return mover | t, nil
}
