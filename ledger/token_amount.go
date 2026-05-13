package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

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

// WithTokenAmount appends a tokenAmount(tag, amount) constraint at the
// next free slot of the output being built. Multiple instances per UTXO
// are allowed per claude/native_token.md §3.
func (o *OutputBuilder) WithTokenAmount(tag base.ChainID, amount uint64) *OutputBuilder {
	o.MustPushConstraint(NewTokenAmount(tag, amount).Bytes())
	return o
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
