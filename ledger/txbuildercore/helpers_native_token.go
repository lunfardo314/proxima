package txbuildercore

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/ledger/base"
)

// Foundry + native-token constraint symbols + source templates.
// Mirror ledger/foundry.go and ledger/native_token.go byte-for-byte.
const (
	// FoundryName is the canonical 1-arg constraint at slot 4 of a
	// foundry chain output: the circulating supply for that tag.
	// The tag itself is read from the sibling chain constraint at
	// slot 3, not from foundry.
	FoundryName = "foundry"

	foundryTemplate = FoundryName + "(z64/%d)"

	// SymToken is the symbol of the tx-level native-token declaration:
	//   token(tag, foundryProducedIdx)
	// where foundryProducedIdx == FoundryIdxNone means pure
	// conservation (no foundry transit for this tag).
	SymToken = "token"

	// tokenSourceTemplate matches the form used by
	// ledger.TokenFoundryBytecode: two inline-data literals (32-byte
	// tag + 1-byte index, the index as a 2-hex-char literal).
	tokenSourceTemplate = SymToken + "(0x%s, 0x%02x)"

	// TokenAmountName is the per-output 2-arg constraint that
	// accounts a value of tag T on this UTXO.
	TokenAmountName = "tokenAmount"

	tokenAmountTemplate = TokenAmountName + "(0x%s, z64/%d)"
)

// FoundryIdxNone is the reserved foundryProducedIndex value meaning
// "no foundry transit for this tag in this tx" — the pure
// conservation form. Mirrors ledger.FoundryIdxNone.
const FoundryIdxNone byte = 0xFF

// NewFoundryBytecode emits the 1-arg foundry(z64/supply) constraint
// at slot 4 of a foundry chain output.
func (l *Library) NewFoundryBytecode(supply uint64) ([]byte, error) {
	return l.CompileExpression(fmt.Sprintf(foundryTemplate, supply))
}

// TokenSentinel emits the tx-level pure-conservation native-token
// declaration for tag T:
//
//	token(0x<tag>, 0xFF)
//
// Push via TxBuilder.PushTxConstraint when the tx moves tokens of
// tag T without touching any foundry (the closing balance equation
// must be exact: in == out for tag T).
func (l *Library) TokenSentinel(tag base.ChainID) ([]byte, error) {
	return l.TokenFoundry(tag, FoundryIdxNone)
}

// TokenFoundry emits the tx-level foundry-transit native-token
// declaration:
//
//	token(0x<tag>, 0x<foundryProducedIdx>)
//
// Push when the tx mints / burns via the produced foundry at the
// given output index. foundryProducedIdx == FoundryIdxNone (0xFF)
// is the pure-conservation form (equivalent to TokenSentinel).
func (l *Library) TokenFoundry(tag base.ChainID, foundryProducedIdx byte) ([]byte, error) {
	src := fmt.Sprintf(tokenSourceTemplate, hex.EncodeToString(tag[:]), foundryProducedIdx)
	return l.CompileExpression(src)
}

// NewTokenAmountBytecode emits a tokenAmount(0x<tag>, z64/<amount>)
// constraint for a produced output. Multiple tokenAmount entries per
// UTXO are allowed (one per tag carried). Use
// AppendTokenAmountToOutput to also mirror the compound-index-value
// side-effect ledger.OutputBuilder.WithTokenAmount applies.
func (l *Library) NewTokenAmountBytecode(tag base.ChainID, amount uint64) ([]byte, error) {
	src := fmt.Sprintf(tokenAmountTemplate, hex.EncodeToString(tag[:]), amount)
	return l.CompileExpression(src)
}

// AppendTokenAmountToOutput appends the tokenAmount(tag, amount)
// constraint to b AND — if slot 1 already carries a primary
// controller (i.e. the lock has been written) — appends a 64-byte
// `controller || tag` compound entry to slot 1, deduplicated by
// byte equality.
//
// This mirrors ledger.OutputBuilder.WithTokenAmount byte-for-byte
// so the server's indexer still emits "my UTXOs holding T" trie
// rows under TriePartitionControllers.
//
// IMPORTANT: call AFTER the lock has been written to slot 1.
// WithLock overwrites slot 1; a compound entry added before would
// be lost.
func (l *Library) AppendTokenAmountToOutput(b *OutputBuilder, tag base.ChainID, amount uint64) error {
	bin, err := l.NewTokenAmountBytecode(tag, amount)
	if err != nil {
		return err
	}
	b.MustPushConstraint(bin)
	appendCompoundIndexValue(b, tag)
	return nil
}

// appendCompoundIndexValue mirrors ledger.OutputBuilder.addCompoundIndexValue:
// appends slot-1[0] || tag (64 bytes) to slot 1, dedup'd. No-op if
// slot 1 is absent or its first entry is empty.
func appendCompoundIndexValue(b *OutputBuilder, tag base.ChainID) {
	bin, err := b.Tuple().At(int(ConstraintIndexIndexValues))
	if err != nil || len(bin) == 0 {
		return
	}
	current, err := decodeIndexValuesTuple(bin)
	if err != nil || len(current) == 0 || len(current[0]) == 0 {
		return
	}
	compound := make([]byte, 0, len(current[0])+len(tag))
	compound = append(compound, current[0]...)
	compound = append(compound, tag[:]...)
	for _, v := range current {
		if bytes.Equal(v, compound) {
			return
		}
	}
	current = append(current, compound)
	b.PutConstraint(EncodeIndexValuesTuple(current), ConstraintIndexIndexValues)
}

// decodeIndexValuesTuple is the inverse of EncodeIndexValuesTuple:
// parse the serialised tuple at slot 1 back to its element slice.
// Empty bytes -> empty slice. Mirrors ledger.IndexValuesFromBytes.
func decodeIndexValuesTuple(data []byte) ([][]byte, error) {
	if len(data) == 0 {
		return nil, nil
	}
	t, err := tuples.TupleFromBytes(data, MaxNumConstraints)
	if err != nil {
		return nil, err
	}
	ret := make([][]byte, 0, t.NumElements())
	t.ForEach(func(_ int, v []byte) bool {
		ret = append(ret, v)
		return true
	})
	return ret, nil
}
