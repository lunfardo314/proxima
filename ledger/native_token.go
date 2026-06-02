// Native (tagged) tokens — design and implementation in one file.
//
// Three concerns live here:
//
//   - NativeTokenAggregator (per-tx cache):
//     One entry per declared tag, holding the foundry transit's supply
//     delta (split into magnitude + burn-flag so every arithmetic op
//     stays in uint64) plus the running consumed / produced sums.
//
//   - token(tag, foundryProducedIdx) — tx-level Go builtin (evalToken):
//     Declares a tag and records its delta. foundryProducedIdx == 0xFF
//     is the pure-conservation sentinel (delta = 0); any other byte
//     names a produced foundry output whose chain ID must equal tag.
//     token() does NOT enforce balance — it only declares.
//
//   - tokenAmount(tag, amount) — UTXO-level Go builtin (evalTokenAmount):
//     Fails if its tag was not declared; otherwise increments the per-
//     tag consumed-or-produced sum (side derived from the eval path),
//     overflow-checked at the call site.
//
// Closing balance equation lives in NativeTokenAggregator.CheckBalances
// and is invoked once at the tail of validateOutputs.
//
// See claude/native_token.md for the design rationale.
package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// ---------------------------------------------------------------------
// NativeTokenAggregator — per-tx cache
// ---------------------------------------------------------------------

// NativeTokenAggregator is the per-tx native-token cache. Each declared
// tag gets one entry holding the foundry transit's supply delta and the
// running consumed/produced sums.
//
// Population model is "every constraint accounts for itself":
//   - `token(tag, foundryIdx)` calls Declare with the tag and its delta
//     (one entry per call, duplicate declarations rejected).
//   - `tokenAmount(tag, amount)` looks up the entry; fails if missing;
//     pre-checks overflow against MaxUint64; then mutates the side
//     running sum directly.
//
// No scanner, no post-hoc audit: validity is local at each constraint;
// the only tx-wide step is CheckBalances at the tail of validateOutputs.
//
// All state is per-tx and accessed single-threadedly by the EasyFL
// evaluator — no mutex required.
type NativeTokenAggregator struct {
	entries map[base.ChainID]*NativeTokenEntry
}

// NativeTokenEntry holds the per-tag running state. Pointer is handed
// back from Entry() so the tokenAmount constraint can do its own
// pre-check and in-place increment of ConsumedSum / ProducedSum.
type NativeTokenEntry struct {
	// supplyDelta == producedFoundrySupply − consumedFoundrySupply,
	// stored as (DeltaMag, DeltaIsBurn) so every arithmetic op stays
	// in uint64 with explicit pre-check overflow detection.
	DeltaMag    uint64
	DeltaIsBurn bool
	ConsumedSum uint64
	ProducedSum uint64
}

func NewNativeTokenAggregator() *NativeTokenAggregator {
	return &NativeTokenAggregator{entries: map[base.ChainID]*NativeTokenEntry{}}
}

// Declare records a token(tag, ...) invocation. deltaMag is the absolute
// value of the foundry transit's supply change; deltaIsBurn is true when
// produced < consumed (burn). For the pure-conservation sentinel form
// pass (0, false). Duplicate declarations within the same tx are
// rejected — a double declaration is always a builder bug.
func (a *NativeTokenAggregator) Declare(tag base.ChainID, deltaMag uint64, deltaIsBurn bool) error {
	if _, ok := a.entries[tag]; ok {
		return fmt.Errorf("native token tag %s declared twice in the same tx", tag.String())
	}
	a.entries[tag] = &NativeTokenEntry{DeltaMag: deltaMag, DeltaIsBurn: deltaIsBurn}
	return nil
}

// Entry returns the entry for tag, or nil if the tag was not declared.
// Caller (the tokenAmount constraint) is responsible for the
// "undeclared" failure and for overflow-safe mutation of the running
// sums.
func (a *NativeTokenAggregator) Entry(tag base.ChainID) *NativeTokenEntry {
	return a.entries[tag]
}

// CheckBalances validates every declared tag's balance equation:
//
//	mint:  producedSum == consumedSum + deltaMag
//	burn:  consumedSum == producedSum + deltaMag
//
// Each addition has an explicit pre-check overflow guard. Returns the
// first failure; success otherwise.
func (a *NativeTokenAggregator) CheckBalances() error {
	for tag, e := range a.entries {
		if e.DeltaIsBurn {
			if e.ProducedSum > math.MaxUint64-e.DeltaMag {
				return fmt.Errorf("native token balance overflow for tag %s (produced=%d + burn=%d)",
					tag.String(), e.ProducedSum, e.DeltaMag)
			}
			expected := e.ProducedSum + e.DeltaMag
			if e.ConsumedSum != expected {
				return fmt.Errorf("native token balance mismatch for tag %s: consumed=%d, produced=%d, burn=%d",
					tag.String(), e.ConsumedSum, e.ProducedSum, e.DeltaMag)
			}
		} else {
			if e.ConsumedSum > math.MaxUint64-e.DeltaMag {
				return fmt.Errorf("native token balance overflow for tag %s (consumed=%d + mint=%d)",
					tag.String(), e.ConsumedSum, e.DeltaMag)
			}
			expected := e.ConsumedSum + e.DeltaMag
			if e.ProducedSum != expected {
				return fmt.Errorf("native token balance mismatch for tag %s: consumed=%d, produced=%d, mint=%d",
					tag.String(), e.ConsumedSum, e.ProducedSum, e.DeltaMag)
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------
// token(tag, foundryProducedIdx) — tx-level Go builtin
// ---------------------------------------------------------------------

// SymToken is the public symbol of the tx-level native-token
// preservation constraint.
const SymToken = "token"

// FoundryIdxNone is the reserved foundryProducedIndex value meaning
// "no foundry transit for this tag in this tx" — the pure conservation
// form. A produced foundry at output index 0xFF is therefore
// unreachable as a token() target; practical foundry transits are
// bounded to indices 0..254 (max-outputs is 256 anyway).
const FoundryIdxNone byte = 0xFF

// TokenSentinelBytecode returns the compiled bytecode for
// `token(tag, 0xFF)` — pure-conservation form. Suitable for
// PushTxConstraint on the TxBuilder.
func TokenSentinelBytecode(tag base.ChainID) []byte {
	return TokenFoundryBytecode(tag, FoundryIdxNone)
}

// TokenFoundryBytecode returns the compiled bytecode for
// `token(tag, foundryProducedIdx)`. If foundryProducedIdx is
// FoundryIdxNone the result is the pure-conservation sentinel form.
func TokenFoundryBytecode(tag base.ChainID, foundryProducedIdx byte) []byte {
	return mustBinFromSource(fmt.Sprintf("%s(0x%s, 0x%02x)", SymToken, hex.EncodeToString(tag[:]), foundryProducedIdx))
}

// evalToken implements the tx-level `token(tag, foundryProducedIdx)`
// constraint. See claude/native_token.md §4.
//
// Fixed-arity 2:
//   - arg 0 (tag):               24-byte inline-data literal (chainID).
//   - arg 1 (foundryProducedIdx): 1-byte inline-data literal.
//     FoundryIdxNone (0xFF) = pure conservation (delta = 0); any other
//     byte names a produced foundry output. For the transit form
//     token() verifies the produced output's chain ID == tag, reaches
//     the consumed predecessor via the chain constraint, reads both
//     supplies, and records the supply delta on the per-tx cache.
//
// token() does NOT enforce the balance equation. Its only job is to
// declare the tag with its delta. Balance is enforced later: each
// tokenAmount(tag, amount) accounts itself onto the declared entry at
// evaluation time, and the tail of validateOutputs calls
// agg.CheckBalances over the cache. Local constraints carry their own
// validity; the global step is just the closing equation.
func evalToken(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()

	// Soundness: token() only fires from the tx-level constraints list.
	if !bytes.HasPrefix(ctx.EvalPath(), PathToTxConstraints) {
		par.TracePanic("token: must be invoked at TxConstraints (path %x), not %x",
			PathToTxConstraints, ctx.EvalPath())
	}

	// arg 0 (tag): 24-byte inline literal (chainID).
	tagExpr := par.ArgExpression(0)
	if !tagExpr.IsInlineData() {
		par.TracePanic("token: arg 0 (tag) must be inline-data literal")
	}
	tagBytes := tagExpr.InlineData()
	if len(tagBytes) != base.ChainIDLength {
		par.TracePanic("token: arg 0 (tag) must be %d-byte literal, got %d", base.ChainIDLength, len(tagBytes))
	}
	var tag base.ChainID
	copy(tag[:], tagBytes)

	// arg 1 (foundryProducedIdx): single-byte inline literal.
	fpiExpr := par.ArgExpression(1)
	if !fpiExpr.IsInlineData() {
		par.TracePanic("token: arg 1 (foundryProducedIdx) must be inline-data literal")
	}
	fpiBytes := fpiExpr.InlineData()
	if len(fpiBytes) != 1 {
		par.TracePanic("token: arg 1 (foundryProducedIdx) must be a single byte, got %d", len(fpiBytes))
	}
	foundryProducedIdx := fpiBytes[0]

	agg := ctx.NativeTokenAggregator()

	if foundryProducedIdx == FoundryIdxNone {
		// Pure conservation: no foundry transit, delta = 0.
		if err := agg.Declare(tag, 0, false); err != nil {
			par.TracePanic("token: %v", err)
		}
		return par.AllocData(0x01)
	}

	// Foundry-transit form: read tag off the produced foundry's chain
	// constraint, reach the consumed predecessor, compute the supply
	// delta. chain() enforces ChainID preservation across every transit,
	// so once produced.chain.ChainID matches tag the consumed
	// predecessor's chain ID is automatically the same (real ID at
	// later transits, derived from predOid at the first transit).
	producedOut, err := ctx.ProducedOutputAt(foundryProducedIdx)
	if err != nil {
		par.TracePanic("token: produced foundry at idx %d: %v", foundryProducedIdx, err)
	}
	pcc := producedOut.ChainConstraint()
	if pcc == nil {
		par.TracePanic("token: produced foundry %d has no chain constraint", foundryProducedIdx)
	}
	// At origin pcc.ChainID == NilChainID, which cannot equal any real
	// user-supplied tag — origin foundry outputs cannot be transit
	// targets (initial supply is 0; minting happens at later transits).
	if pcc.ChainID != tag {
		par.TracePanic("token: produced foundry chain ID %s does not match token tag %s",
			pcc.ChainID.String(), tag.String())
	}
	pfBytes, err := producedOut.ConstraintAt(ConstraintIndexFoundry)
	if err != nil {
		par.TracePanic("token: produced output %d has no constraint at foundry slot %d: %v",
			foundryProducedIdx, ConstraintIndexFoundry, err)
	}
	pf, err := FoundryFromBytesWithLib(pfBytes, ctx.GetLibrary())
	if err != nil {
		par.TracePanic("token: parse produced foundry: %v", err)
	}

	// Reach the consumed foundry through the chain predecessor.
	consumedOut, err := ctx.ConsumedOutput(pcc.PredecessorInputIndex)
	if err != nil {
		par.TracePanic("token: consumed foundry at input idx %d: %v", pcc.PredecessorInputIndex, err)
	}
	cfBytes, err := consumedOut.ConstraintAt(ConstraintIndexFoundry)
	if err != nil {
		par.TracePanic("token: consumed foundry predecessor has no constraint at foundry slot %d: %v",
			ConstraintIndexFoundry, err)
	}
	cf, err := FoundryFromBytesWithLib(cfBytes, ctx.GetLibrary())
	if err != nil {
		par.TracePanic("token: parse consumed foundry: %v", err)
	}

	// Compute |delta| + burn-flag using unsigned arithmetic only —
	// always subtracting smaller from larger so no underflow.
	var deltaMag uint64
	var deltaIsBurn bool
	if pf.Supply >= cf.Supply {
		deltaMag = pf.Supply - cf.Supply
	} else {
		deltaMag = cf.Supply - pf.Supply
		deltaIsBurn = true
	}
	if err := agg.Declare(tag, deltaMag, deltaIsBurn); err != nil {
		par.TracePanic("token: %v", err)
	}
	return par.AllocData(0x01)
}

// ---------------------------------------------------------------------
// tokenAmount(tag, amount) — UTXO-level Go builtin + serde wrapper
// ---------------------------------------------------------------------

// TokenAmount is the typed wrapper for the 2-arg `tokenAmount(tag,
// amount)` constraint. It lives at any non-reserved tuple position of a
// non-foundry UTXO that holds native tokens; multiple instances per
// output are permitted. See claude/native_token.md §3.
type TokenAmount struct {
	// Tag is the foundry chain ID this amount is denominated in.
	Tag base.ChainID
	// Amount is the carried quantity (uint64, > 0).
	Amount uint64
}

const (
	TokenAmountName     = "tokenAmount"
	tokenAmountTemplate = TokenAmountName + "(0x%s, z64/%d)"
)

func NewTokenAmount(tag base.ChainID, amount uint64) *TokenAmount {
	return &TokenAmount{Tag: tag, Amount: amount}
}

// WithTokenAmount appends a tokenAmount(tag, amount) constraint to the
// output being built and, if slot 1 already carries a primary controller
// (i.e. WithLock has been called and produced at least one index value),
// appends a single 64-byte compound entry `controller || tag` to slot 1
// — deduplicated. Mutate.go's indexer is untouched: it iterates slot 1
// values and emits one trie entry per non-empty element under
// TriePartitionControllers; the length byte (32 for the lock's own
// controller, 64 for our compound key) lets a wallet prefix-iterate
// "my UTXOs holding tag T" via key `holderID || tag` without ever
// confusing it with a 32-byte single-controller entry.
//
// Multiple tokenAmount instances per UTXO are allowed per
// claude/native_token.md §3; duplicate (controller, tag) pairs are
// collapsed to a single compound entry in slot 1.
//
// IMPORTANT: call after WithLock. WithLock overwrites slot 1, so any
// compound entry added before will be lost.
func (o *OutputBuilder) WithTokenAmount(tag base.ChainID, amount uint64) *OutputBuilder {
	o.MustPushConstraint(NewTokenAmount(tag, amount).Bytes())
	o.addCompoundIndexValue(tag)
	return o
}

// addCompoundIndexValue appends `slot-1[0] || tag` (64 bytes) to the
// slot-1 index-value tuple, deduplicating by byte equality. No-op if
// slot 1 is absent or its first entry is empty.
func (o *OutputBuilder) addCompoundIndexValue(tag base.ChainID) {
	bin, err := o.Tuple().At(int(ConstraintIndexIndexValues))
	if err != nil || len(bin) == 0 {
		return
	}
	current, err := IndexValuesFromBytes(bin)
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
	o.PutConstraint(IndexValuesTupleBytes(current), ConstraintIndexIndexValues)
}

func (t *TokenAmount) Name() string { return TokenAmountName }

func (t *TokenAmount) Source() string {
	return fmt.Sprintf(tokenAmountTemplate, hex.EncodeToString(t.Tag[:]), t.Amount)
}

func (t *TokenAmount) Bytes() []byte { return mustBinFromSource(t.Source()) }

func (t *TokenAmount) String() string {
	return fmt.Sprintf("%s(tag=%s, amount=%d)", TokenAmountName, t.Tag.String(), t.Amount)
}

// TokenAmountFromBytes parses the 2-arg tokenAmount bytecode.
func TokenAmountFromBytes(data []byte) (*TokenAmount, error) {
	return TokenAmountFromBytesWithLib(data, L(base.MaxSlot))
}

func TokenAmountFromBytesWithLib(data []byte, lib *Library) (*TokenAmount, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 2)
	if err != nil {
		return nil, fmt.Errorf("TokenAmountFromBytes: %w", err)
	}
	if sym != TokenAmountName {
		return nil, fmt.Errorf("TokenAmountFromBytes: not a tokenAmount")
	}
	ret := &TokenAmount{}
	if ret.Tag, err = base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0])); err != nil {
		return nil, fmt.Errorf("TokenAmountFromBytes: %w", err)
	}
	if ret.Amount, err = easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1])); err != nil {
		return nil, fmt.Errorf("TokenAmountFromBytes: %w", err)
	}
	if ret.Amount == 0 {
		return nil, fmt.Errorf("TokenAmountFromBytes: amount must be > 0")
	}
	return ret, nil
}

func registerTokenAmount(lib *Library) {
	lib.mustRegisterConstraint(TokenAmountName, 2, func(data []byte) (Constraint, error) {
		return TokenAmountFromBytesWithLib(data, lib)
	})
}

// evalTokenAmount implements the UTXO-level `tokenAmount(tag, amount)`
// constraint as a Go builtin. See claude/native_token.md §3.
//
// Local rules enforced at every invocation:
//
//  1. arg 0 (tag): 24-byte inline-data literal (chainID).
//  2. arg 1 (amount): inline-data literal, decodes to uint64 > 0.
//  3. The tag MUST have been declared at the tx level by a matching
//     token(...) call (the only sequencing assumption: tx-level
//     constraints run before per-output constraints).
//
// Side effect (after all local checks pass): increment the per-tag
// running sum on the corresponding side (consumed vs produced,
// determined by the eval path), pre-checking against MaxUint64 to
// reject any addition that would overflow.
//
// This constraint does NOT enforce the closing balance equation. That
// is one tight per-declared-tag loop run by agg.CheckBalances at the
// tail of validateOutputs.
func evalTokenAmount(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()
	path := ctx.EvalPath()
	isConsumed := bytes.HasPrefix(path, PathToConsumedOutputs)
	isProduced := bytes.HasPrefix(path, PathToProducedOutputs)
	if !isConsumed && !isProduced {
		par.TracePanic("tokenAmount: must be invoked on a consumed or produced output (path %x)", path)
	}

	// arg 0 (tag): 24-byte inline literal (chainID).
	tagExpr := par.ArgExpression(0)
	if !tagExpr.IsInlineData() {
		par.TracePanic("tokenAmount: arg 0 (tag) must be inline-data literal")
	}
	tagBytes := tagExpr.InlineData()
	if len(tagBytes) != base.ChainIDLength {
		par.TracePanic("tokenAmount: arg 0 (tag) must be %d-byte literal, got %d", base.ChainIDLength, len(tagBytes))
	}
	var tag base.ChainID
	copy(tag[:], tagBytes)

	// arg 1 (amount): inline literal, > 0.
	amtExpr := par.ArgExpression(1)
	if !amtExpr.IsInlineData() {
		par.TracePanic("tokenAmount: arg 1 (amount) must be inline-data literal")
	}
	amount, err := easyfl_util.Uint64FromBytes(amtExpr.InlineData())
	if err != nil {
		par.TracePanic("tokenAmount: amount decode: %v", err)
	}
	if amount == 0 {
		par.TracePanic("tokenAmount: amount must be > 0")
	}

	// Tag must have been declared at the tx level.
	entry := ctx.NativeTokenAggregator().Entry(tag)
	if entry == nil {
		par.TracePanic("tokenAmount: tag %s not declared at tx level (missing token(...) call)", tag.String())
	}

	// Pre-check overflow at the call site and increment the side sum.
	if isConsumed {
		if entry.ConsumedSum > math.MaxUint64-amount {
			par.TracePanic("tokenAmount: consumed sum overflow for tag %s (%d + %d)",
				tag.String(), entry.ConsumedSum, amount)
		}
		entry.ConsumedSum += amount
	} else {
		if entry.ProducedSum > math.MaxUint64-amount {
			par.TracePanic("tokenAmount: produced sum overflow for tag %s (%d + %d)",
				tag.String(), entry.ProducedSum, amount)
		}
		entry.ProducedSum += amount
	}
	return par.AllocData(0x01)
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip a tokenAmount with a random tag and a non-trivial amount.
		tag := base.RandomChainID()
		example := NewTokenAmount(tag, 12345)
		back, err := TokenAmountFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(back.Tag == example.Tag, "tokenAmount tag round-trip")
		util.Assertf(back.Amount == example.Amount, "tokenAmount amount round-trip")
		util.Assertf(EqualConstraints(example, back), "inconsistency in "+TokenAmountName)

		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
		pref2, err := lib.EvalFromSource(nil, "#"+TokenAmountName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "tokenAmount prefix match")

		// Amount must be > 0: a zero-amount instance round-trip must fail.
		zero := &TokenAmount{Tag: tag, Amount: 0}
		_, err = TokenAmountFromBytesWithLib(zero.Bytes(), lib)
		util.Assertf(err != nil, "zero amount must be rejected by parser")
	})
}
