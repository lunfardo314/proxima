package chess_poc

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
)

// =============================================================================
// Constants — branch selectors / flag bits / output slot indices
// =============================================================================

// Output element index for the chessState tuple (per chess_poc.md §2).
const ChessStateConstraintIndex = byte(4)

// Branch selector (byte 0 of the lock's unlock parameters).
const (
	BranchMove         byte = 0x00
	BranchTieAccept    byte = 0x01
	BranchResign       byte = 0x02
	BranchTimeoutClaim byte = 0x03
)

// Flags byte (chessState idx 6).
const (
	FlagTieProposed byte = 0x01
	flagsReserved   byte = 0xfe // bits 1..7
)

// chessValidator color bytes (mirror chess_script.easyfl).
const (
	SideWhite byte = 0x10
	SideBlack byte = 0x20
)

// Canonical 69-byte starting board (mirrors chess_script.md §1 / isStart).
// Same bytes as chessGame's _canonicalStart constant.
//   ranks 0-1: 16 bytes (white back rank + pawns)
//   ranks 2-5: 32 bytes (empty)
//   ranks 6-7: 16 bytes (black pawns + back rank)
//   trailing:   5 bytes (wKsq, bKsq, castling, EP, side)
var CanonicalStartBoard = mustHexLit(
	"14121315161312141111111111111111" +
		"00000000000000000000000000000000" +
		"00000000000000000000000000000000" +
		"21212121212121212422232526232224" +
		"043c0fff10")

func mustHexLit(s string) []byte {
	b := make([]byte, len(s)/2)
	for i := 0; i < len(s); i += 2 {
		var hi, lo byte
		hi = unhex(s[i])
		lo = unhex(s[i+1])
		b[i/2] = hi<<4 | lo
	}
	if len(b) != 69 {
		panic(fmt.Sprintf("CanonicalStartBoard must be 69 bytes, got %d", len(b)))
	}
	return b
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	panic("bad hex digit")
}

// =============================================================================
// ChessState — 7-tuple at output element index 4
// =============================================================================

// ChessState is the per-UTXO state of an in-progress chess game.
// Wire form is a 7-element easyfl tuple, indexed by atTuple8 inside chessGame.
// Field byte sizes are enforced by the redeemer; unmarshal mirrors them.
type ChessState struct {
	Board        []byte           // 69 bytes
	LastMoveSpec []byte           // 5 bytes
	WhiteHolder  base.HolderID    // 32 bytes
	BlackHolder  []byte           // 0 bytes (pre-acceptance) or 32 bytes
	TSlots       uint32           // big-endian 4 bytes
	Deadline     base.LedgerTime  // 5 bytes (slot+tick); tick MUST be 0
	Flags        byte             // bit 0 = tieProposed; bits 1..7 reserved
}

// Marshal returns the EasyFL bytecode for output element index 4. The wire
// form is just an inline-data literal carrying the serialised 7-element
// easyfl tuple [board, spec, white, black, T, deadline, flags].
//
// Evaluating an inline-data literal as bytecode returns its payload — i.e.
// the tuple bytes — which is non-empty and so satisfies the framework's
// "every output-slot constraint must evaluate truthy" rule. No wrapping
// call (`or`, `if`, etc.) needed. chessGame reads fields via
// atTuple8(parseInlineData(selfSiblingConstraint(4)), i).
func (s *ChessState) Marshal() []byte {
	tSlotsBin := make([]byte, 4)
	binary.BigEndian.PutUint32(tSlotsBin, s.TSlots)

	t := tuples.EmptyTupleEditable(256)
	t.MustPush(s.Board)
	t.MustPush(s.LastMoveSpec)
	t.MustPush(s.WhiteHolder[:])
	t.MustPush(append([]byte(nil), s.BlackHolder...))
	t.MustPush(tSlotsBin)
	t.MustPush(s.Deadline.Bytes())
	t.MustPush([]byte{s.Flags})

	return easyfl.InlineDataBytecode(t.Tuple().Bytes())
}

// UnmarshalChessState parses chessState bytecode (as produced by Marshal)
// back into a struct. Strips the inline-data prefix to recover the raw
// tuple bytes, then decodes each tuple element.
func UnmarshalChessState(bytecode []byte) (*ChessState, error) {
	tupleBytes := easyfl.StripDataPrefix(bytecode)
	if tupleBytes == nil {
		return nil, fmt.Errorf("UnmarshalChessState: not an inline-data literal")
	}
	t, err := tuples.TupleFromBytes(tupleBytes, 256)
	if err != nil {
		return nil, fmt.Errorf("UnmarshalChessState: parse tuple: %w", err)
	}
	if t.NumElements() != 7 {
		return nil, fmt.Errorf("UnmarshalChessState: want 7 elements, got %d", t.NumElements())
	}
	get := func(i int) []byte { v, e := t.At(i); if e != nil { panic(e) }; return v }

	board := get(0)
	spec := get(1)
	white := get(2)
	black := get(3)
	tslotsBin := get(4)
	deadlineBin := get(5)
	flagsBin := get(6)

	if len(board) != 69 {
		return nil, fmt.Errorf("UnmarshalChessState: board must be 69 bytes, got %d", len(board))
	}
	if len(spec) != 5 {
		return nil, fmt.Errorf("UnmarshalChessState: lastMoveSpec must be 5 bytes, got %d", len(spec))
	}
	if len(white) != 32 {
		return nil, fmt.Errorf("UnmarshalChessState: whiteHolderID must be 32 bytes, got %d", len(white))
	}
	if len(black) != 0 && len(black) != 32 {
		return nil, fmt.Errorf("UnmarshalChessState: blackHolderID must be empty or 32 bytes, got %d", len(black))
	}
	if len(tslotsBin) != 4 {
		return nil, fmt.Errorf("UnmarshalChessState: T_slots must be 4 bytes, got %d", len(tslotsBin))
	}
	if len(deadlineBin) != 5 {
		return nil, fmt.Errorf("UnmarshalChessState: deadline must be 5 bytes, got %d", len(deadlineBin))
	}
	if len(flagsBin) != 1 {
		return nil, fmt.Errorf("UnmarshalChessState: flags must be 1 byte, got %d", len(flagsBin))
	}
	deadline, err := base.LedgerTimeFromBytes(deadlineBin)
	if err != nil {
		return nil, fmt.Errorf("UnmarshalChessState: %w", err)
	}

	ret := &ChessState{
		Board:        append([]byte(nil), board...),
		LastMoveSpec: append([]byte(nil), spec...),
		TSlots:       binary.BigEndian.Uint32(tslotsBin),
		Deadline:     deadline,
		Flags:        flagsBin[0],
		BlackHolder:  append([]byte(nil), black...),
	}
	copy(ret.WhiteHolder[:], white)
	return ret, nil
}

// SideToMove returns the side byte at board index 68 (0x10 white / 0x20 black).
func (s *ChessState) SideToMove() byte {
	if len(s.Board) != 69 {
		return 0
	}
	return s.Board[68]
}
