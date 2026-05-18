package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
)

// SymToken is the public symbol of the tx-level native-token
// preservation constraint.
const SymToken = "token"

// TokenSentinelBytecode returns the compiled bytecode for
// `token(tag, 0x)` — pure-conservation form (no foundry transit).
// Suitable for PushTxConstraint on the TxBuilder.
func TokenSentinelBytecode(tag base.ChainID) []byte {
	return mustBinFromSource(fmt.Sprintf("%s(0x%s, 0x)", SymToken, hex.EncodeToString(tag[:])))
}

// TokenFoundryBytecode returns the compiled bytecode for
// `token(tag, foundryProducedIdx)` — foundry-transit form.
func TokenFoundryBytecode(tag base.ChainID, foundryProducedIdx byte) []byte {
	return mustBinFromSource(fmt.Sprintf("%s(0x%s, 0x%02x)", SymToken, hex.EncodeToString(tag[:]), foundryProducedIdx))
}

// evalToken implements the token(<tag>, <foundryProducedIndex>) tx-level
// constraint — the per-tag analogue of the PRXI conservation check. See
// claude/native_token.md §4.
//
// Both args must be inline-data literals. Arg 0 (tag) is a 32-byte chain
// ID. Arg 1 (foundryProducedIndex) is empty (sentinel) for pure
// conservation, or a single byte naming the produced foundry output
// index for a mint/burn transit.
//
// Behaviour:
//   - First call in a tx triggers a single scan of all consumed and
//     produced outputs to populate the per-tag aggregator. Subsequent
//     calls hit the cache.
//   - Records the tag in the declared set on the tx (Phase D consumes
//     this for the auditability check).
//   - Sentinel form: enforces Σ consumed(tag) == Σ produced(tag).
//   - Foundry-transit form: reads the foundry's tag off the sibling
//     chain constraint at the produced foundry output (the foundry has
//     no tag arg of its own — the chain constraint IS the tag), confirms
//     it matches `tag`, reads the produced foundry's supply from
//     foundry(supply), reads the consumed predecessor's supply
//     (origin = 0), and enforces the balance equation
//       Σ consumed(tag) + (producedSupply − consumedSupply) == Σ produced(tag).
//
// Note: chain() enforces ChainID preservation across every transit, so
// once the produced chain.ChainID matches the requested tag, the
// consumed predecessor's chain.ChainID is guaranteed to be the same
// (real ID at later transits, derived from predOid at first transit).
// Any extras after the foundry (e.g. an optional policy script at
// foundryPolicyConstraintIndex) are auto-evaluated as part of the
// standard output-tuple validation pass; foundry() does NOT enforce
// immutability of those extras across transit — it is the policy
// script's own responsibility to self-lock if desired (typically via
// `selfImmutableOnSuccessorIndex(...)` in chain.easyfl).
func evalToken(par *easyfl.CallParams[*EvalContext]) []byte {
	ctx := par.DataContext()

	// Soundness: token() only fires from the tx-level constraints list.
	if !bytes.HasPrefix(ctx.EvalPath(), PathToTxConstraints) {
		par.TracePanic("token: must be invoked at TxConstraints (path %x), not %x",
			PathToTxConstraints, ctx.EvalPath())
	}

	// Auditability: arg 0 (tag) must be a 32-byte inline-data literal.
	tagExpr := par.ArgExpression(0)
	if !tagExpr.IsInlineData() {
		par.TracePanic("token: arg 0 (tag) must be inline-data literal")
	}
	tagBytes := tagExpr.InlineData()
	if len(tagBytes) != 32 {
		par.TracePanic("token: arg 0 (tag) must be 32-byte literal, got %d", len(tagBytes))
	}
	var tag base.ChainID
	copy(tag[:], tagBytes)

	// Auditability: arg 1 (foundryProducedIndex) must be an inline-data
	// literal. Empty = sentinel (no foundry transit); 1 byte = index of
	// the produced foundry output.
	fpiExpr := par.ArgExpression(1)
	if !fpiExpr.IsInlineData() {
		par.TracePanic("token: arg 1 (foundryProducedIndex) must be inline-data literal")
	}
	fpiBytes := fpiExpr.InlineData()
	var foundryProducedIdx byte
	var hasFoundryTransit bool
	switch len(fpiBytes) {
	case 0:
		// sentinel: no foundry transit
	case 1:
		foundryProducedIdx = fpiBytes[0]
		hasFoundryTransit = true
	default:
		par.TracePanic("token: arg 1 must be empty (sentinel) or 1 byte (foundry index), got %d", len(fpiBytes))
	}

	// Run the lazy aggregator scan (idempotent).
	if err := ScanNativeTokens(ctx); err != nil {
		par.TracePanic("token: %v", err)
	}

	agg := ctx.NativeTokenAggregator()
	agg.Declare(tag)
	consumedSum, producedSum, _ := agg.Sum(tag)

	if !hasFoundryTransit {
		if consumedSum != producedSum {
			par.TracePanic("native token amount mismatch for tag %s: consumed=%d, produced=%d",
				tag.String(), consumedSum, producedSum)
		}
		return par.AllocData(0x01)
	}

	// Foundry transit: locate produced foundry, read tag off its sibling
	// chain constraint, look up consumed foundry through the chain
	// predecessor, enforce the balance equation. The foundry has no tag
	// arg of its own — the chain constraint IS the tag, and chain()
	// already enforces ChainID preservation across every transit.
	producedOut, err := ctx.ProducedOutputAt(foundryProducedIdx)
	if err != nil {
		par.TracePanic("token: produced foundry at idx %d: %v", foundryProducedIdx, err)
	}
	pcc := producedOut.ChainConstraint()
	if pcc == nil {
		par.TracePanic("token: produced foundry %d has no chain constraint", foundryProducedIdx)
	}
	// At origin pcc.ChainID is still NilChainID, which cannot equal any
	// real user-supplied tag — so this check also rejects token() calls
	// pointing at an origin foundry output (origin txs never need them:
	// initial supply is 0).
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

	// Reach the consumed foundry through the chain predecessor. The
	// pcc.ChainID != tag check above already rejected pcc.IsOrigin()
	// (origin chains have ChainID == NilChainID), so a real predecessor
	// always exists here.
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
	consumedSupply := cf.Supply

	// Balance equation: consumedSum + (pf.Supply − consumedSupply) == producedSum.
	// Split on mint vs burn to keep arithmetic in unsigned uint64.
	if pf.Supply >= consumedSupply {
		minted := pf.Supply - consumedSupply
		if producedSum < consumedSum || producedSum-consumedSum != minted {
			par.TracePanic("native token amount mismatch for tag %s: consumed=%d, produced=%d, consumedSupply=%d, producedSupply=%d (expected mint of %d)",
				tag.String(), consumedSum, producedSum, consumedSupply, pf.Supply, minted)
		}
	} else {
		burned := consumedSupply - pf.Supply
		if consumedSum < producedSum || consumedSum-producedSum != burned {
			par.TracePanic("native token amount mismatch for tag %s: consumed=%d, produced=%d, consumedSupply=%d, producedSupply=%d (expected burn of %d)",
				tag.String(), consumedSum, producedSum, consumedSupply, pf.Supply, burned)
		}
	}
	return par.AllocData(0x01)
}

// ScanNativeTokens walks every consumed and produced output once,
// finds tokenAmount(tag, amount) constraints by bytecode prefix, and
// accumulates per-tag sums on the per-tx aggregator. Idempotent: the
// scan runs exactly once per tx, on the first token() call. Returns
// error if any tokenAmount instance has non-literal args, a
// malformed tag, a zero amount, or causes a sum overflow.
func ScanNativeTokens(ctx *EvalContext) error {
	agg := ctx.NativeTokenAggregator()
	if agg.Scanned() {
		return nil
	}
	lib := ctx.GetLibrary()
	prefix, err := lib.FunctionCallPrefixByName(TokenAmountName, 2)
	if err != nil {
		return fmt.Errorf("ScanNativeTokens: get tokenAmount prefix: %w", err)
	}

	// consumed outputs
	for i := 0; i < ctx.NumInputs(); i++ {
		o, err := ctx.ConsumedOutput(byte(i))
		if err != nil {
			return fmt.Errorf("ScanNativeTokens: consumed[%d]: %w", i, err)
		}
		if err := scanOutputForTokenAmount(o, agg, true, prefix, lib); err != nil {
			return fmt.Errorf("ScanNativeTokens: consumed[%d]: %w", i, err)
		}
	}
	// produced outputs
	for i := 0; i < ctx.NumProducedOutputs(); i++ {
		o, err := ctx.ProducedOutputAt(byte(i))
		if err != nil {
			return fmt.Errorf("ScanNativeTokens: produced[%d]: %w", i, err)
		}
		if err := scanOutputForTokenAmount(o, agg, false, prefix, lib); err != nil {
			return fmt.Errorf("ScanNativeTokens: produced[%d]: %w", i, err)
		}
	}
	agg.MarkScanned()
	return nil
}

// scanOutputForTokenAmount finds every tokenAmount(tag, amount) in an
// output tuple and accumulates it on the aggregator. Enforces the
// "args must be inline literals" rule that the design concentrates in
// the token() builtin (see §3 of claude/native_token.md).
func scanOutputForTokenAmount(o *Output, agg *NativeTokenAggregator, isConsumed bool, prefix []byte, lib *Library) error {
	for slotIdx, raw := range o.ConstraintsRawBytes() {
		if !bytes.HasPrefix(raw, prefix) {
			continue
		}
		sym, _, args, err := lib.ParseBytecodeOneLevel(raw, 2)
		if err != nil {
			return fmt.Errorf("slot %d: parse tokenAmount: %w", slotIdx, err)
		}
		if sym != TokenAmountName {
			// Prefix matched but symbol didn't — shouldn't happen but
			// stay defensive.
			continue
		}
		if !easyfl.HasInlineDataPrefix(args[0]) {
			return fmt.Errorf("slot %d: tokenAmount tag must be inline-data literal", slotIdx)
		}
		if !easyfl.HasInlineDataPrefix(args[1]) {
			return fmt.Errorf("slot %d: tokenAmount amount must be inline-data literal", slotIdx)
		}
		tag, err := base.ChainIDFromBytes(easyfl.StripDataPrefix(args[0]))
		if err != nil {
			return fmt.Errorf("slot %d: tokenAmount tag: %w", slotIdx, err)
		}
		amount, err := easyfl_util.Uint64FromBytes(easyfl.StripDataPrefix(args[1]))
		if err != nil {
			return fmt.Errorf("slot %d: tokenAmount amount: %w", slotIdx, err)
		}
		if amount == 0 {
			return fmt.Errorf("slot %d: tokenAmount amount must be > 0", slotIdx)
		}
		if isConsumed {
			if err := agg.AddConsumed(tag, amount); err != nil {
				return fmt.Errorf("slot %d: %w", slotIdx, err)
			}
		} else {
			if err := agg.AddProduced(tag, amount); err != nil {
				return fmt.Errorf("slot %d: %w", slotIdx, err)
			}
		}
	}
	return nil
}
