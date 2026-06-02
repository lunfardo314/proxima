// Package txbuildercore is the TinyGo-clean compose + sign core of Proxima's
// transaction builder. It is the package the wasm wallet imports.
//
// Scope: raw container types (Output, OutputBuilder), low-level
// builder ops, signing, and the wallet-side library wrapper. The
// typed constraint parsers (Lock, ChainConstraint, Amounts, …) plus
// pretty-printers stay in the full ledger package and are reachable
// only from the server build.
//
// Status: Phase 1 — Output + OutputBuilder. See claude/wasm_txbuilder.md.
package txbuildercore

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"golang.org/x/crypto/blake2b"
)

// MaxNumConstraints caps the number of constraint slots in a single
// output tuple. Wire-format limit; do not change without a coordinated
// ledger upgrade.
const MaxNumConstraints = 256

type (
	// Output is an immutable UTXO: a tuple of constraint bytecodes.
	// The wallet sees outputs only as raw constraint slots; typed
	// access (Lock, ChainConstraint, Amounts, …) lives on the ledger
	// embedding in the full build.
	Output struct {
		*tuples.Tuple
	}

	// OutputBuilder is a mutable Output under construction. The raw
	// API (Put / Append / NumConstraints / Bytes / Output) is what the
	// wasm wallet uses; the typed convenience methods (WithLock,
	// WithAmounts, …) live on the ledger embedding.
	OutputBuilder struct {
		*tuples.TupleEditable
	}
)

// NewOutputBuilder returns an empty OutputBuilder ready for Put / Append.
func NewOutputBuilder() *OutputBuilder {
	return &OutputBuilder{TupleEditable: tuples.EmptyTupleEditable(MaxNumConstraints)}
}

// NewOutput builds an Output by invoking buildFun on a fresh
// OutputBuilder. The closure receives the raw txbuildercore.OutputBuilder.
// For ledger-typed ergonomics use ledger.NewOutput.
func NewOutput(buildFun func(o *OutputBuilder)) *Output {
	b := NewOutputBuilder()
	if buildFun != nil {
		buildFun(b)
	}
	return b.Output()
}

// OutputFromBytes parses raw output bytes into an Output without
// applying any constraint-side validation. Use this on compose-side
// callers; the server-side wraps it with typed validation in
// ledger.OutputFromBytes.
func OutputFromBytes(data []byte) (*Output, error) {
	arr, err := tuples.TupleFromBytes(data, MaxNumConstraints)
	if err != nil {
		return nil, fmt.Errorf("txbuildercore.OutputFromBytes: %v", err)
	}
	return &Output{Tuple: arr}, nil
}

// OutputBuilderFromBytes creates a mutable OutputBuilder from
// serialized output bytes.
func OutputBuilderFromBytes(data []byte) (*OutputBuilder, error) {
	ret, err := tuples.TupleFromBytesEditable(data, MaxNumConstraints)
	if err != nil {
		return nil, fmt.Errorf("txbuildercore.OutputBuilderFromBytes: %v", err)
	}
	return &OutputBuilder{TupleEditable: ret}, nil
}

// Output finalises the builder into an immutable Output.
func (b *OutputBuilder) Output() *Output {
	return &Output{Tuple: b.Tuple()}
}

// MustPushConstraint appends a constraint bytecode and returns its
// index. Panics if the cap is hit.
func (b *OutputBuilder) MustPushConstraint(c []byte) byte {
	easyfl_util.Assertf(b.NumElements() < MaxNumConstraints, "too many UTXO elements")
	b.MustPush(c)
	return byte(b.NumElements() - 1)
}

// PutConstraint places constraint bytecode at the given index,
// padding empty slots in between if needed.
func (b *OutputBuilder) PutConstraint(c []byte, idx byte) {
	b.MustPutAtIdxWithPadding(idx, c)
}

// NumConstraints returns the number of constraint slots populated.
func (b *OutputBuilder) NumConstraints() int {
	return b.NumElements()
}

// NumConstraints (on Output) returns the number of constraint slots.
func (o *Output) NumConstraints() int {
	return o.NumElements()
}

// MustConstraintAt returns raw constraint bytecode at the given
// index. Panics if out of range.
func (o *Output) MustConstraintAt(idx byte) []byte {
	return o.MustAt(int(idx))
}

// ConstraintAt returns raw constraint bytecode at the given index.
func (o *Output) ConstraintAt(idx byte) ([]byte, error) {
	return o.At(int(idx))
}

// ConstraintsRawBytes returns a copy slice of all constraint slots
// in declaration order.
func (o *Output) ConstraintsRawBytes() [][]byte {
	ret := make([][]byte, 0, o.NumConstraints())
	o.ForEach(func(_ int, data []byte) bool {
		ret = append(ret, data)
		return true
	})
	return ret
}

// Hex returns the output bytes as a hex string.
func (o *Output) Hex() string {
	return hex.EncodeToString(o.Bytes())
}

// CloneRaw creates a byte-level copy without any constraint-side
// validation. Use this on compose paths for outputs that aren't
// shaped like normal UTXOs (e.g. upgrade UTXOs).
func (o *Output) CloneRaw() *Output {
	arr, err := tuples.TupleFromBytes(bytes.Clone(o.Bytes()), MaxNumConstraints)
	easyfl_util.AssertNoError(err)
	return &Output{Tuple: arr}
}

// HashOutputs computes the blake2b hash of serialized outputs
// (used as the input commitment). Same algorithm as the full ledger
// path; lives here so the wasm core can compute it without dragging
// in the constraint serdes.
func HashOutputs(outs ...*Output) [32]byte {
	arr := tuples.EmptyTupleEditable(MaxNumConstraints)
	for _, o := range outs {
		arr.MustPush(o.Bytes())
	}
	return blake2b.Sum256(arr.Bytes())
}

// HashOutputBytes is the raw-byte form of HashOutputs. Wallet code
// already holds outputs as bytes when computing the input commitment;
// this saves a round-trip through *Output.
func HashOutputBytes(outBytes ...[]byte) [32]byte {
	arr := tuples.EmptyTupleEditable(MaxNumConstraints)
	for _, b := range outBytes {
		arr.MustPush(b)
	}
	return blake2b.Sum256(arr.Bytes())
}
