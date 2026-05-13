package ledger

import (
	"bytes"
	"encoding/hex"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/util"
)

// FoundryPolicy is the typed wrapper for the 1-arg `foundryPolicy(script)`
// constraint. It lives optionally at ConstraintIndexFoundryPolicy (= 5)
// on a foundry output and carries the inline EasyFL bytecode of the
// issuance policy script. The script is **immutable** across foundry
// transits and is evaluated by the Go reconciler on transit (Phase C);
// absence of the constraint ⇒ no on-chain policy beyond the
// controller's signature. See claude/native_token.md issuance policy.
type FoundryPolicy struct {
	// Script is the inline EasyFL bytecode of the policy script.
	Script []byte
}

const (
	FoundryPolicyName     = "foundryPolicy"
	foundryPolicyTemplate = FoundryPolicyName + "(0x%s)"
)

func NewFoundryPolicy(script []byte) *FoundryPolicy {
	return &FoundryPolicy{Script: bytes.Clone(script)}
}

func (p *FoundryPolicy) Name() string { return FoundryPolicyName }

func (p *FoundryPolicy) Source() string {
	return fmt.Sprintf(foundryPolicyTemplate, hex.EncodeToString(p.Script))
}

func (p *FoundryPolicy) Bytes() []byte { return mustBinFromSource(p.Source()) }

func (p *FoundryPolicy) String() string {
	return fmt.Sprintf("%s(script=0x%s)", FoundryPolicyName, hex.EncodeToString(p.Script))
}

// FoundryPolicyFromBytes parses the 1-arg foundryPolicy bytecode.
func FoundryPolicyFromBytes(data []byte) (*FoundryPolicy, error) {
	return FoundryPolicyFromBytesWithLib(data, L(base.MaxSlot))
}

func FoundryPolicyFromBytesWithLib(data []byte, lib *Library) (*FoundryPolicy, error) {
	sym, _, args, err := lib.ParseBytecodeOneLevel(data, 1)
	if err != nil {
		return nil, fmt.Errorf("FoundryPolicyFromBytes: %w", err)
	}
	if sym != FoundryPolicyName {
		return nil, fmt.Errorf("FoundryPolicyFromBytes: not a foundryPolicy")
	}
	script := easyfl.StripDataPrefix(args[0])
	if len(script) == 0 {
		return nil, fmt.Errorf("FoundryPolicyFromBytes: script must be non-empty")
	}
	return &FoundryPolicy{Script: bytes.Clone(script)}, nil
}

func registerFoundryPolicy(lib *Library) {
	lib.mustRegisterConstraint(FoundryPolicyName, 1, func(data []byte) (Constraint, error) {
		return FoundryPolicyFromBytesWithLib(data, lib)
	})
}

func init() {
	registerInlineTest(func(lib *Library) {
		// Round-trip a non-trivial policy bytecode. The contents are
		// opaque at this layer; we just check the wrapper preserves bytes.
		script := []byte{0xff, 0x00, 0x42, 0x07, 0x55}
		example := NewFoundryPolicy(script)
		back, err := FoundryPolicyFromBytesWithLib(example.Bytes(), lib)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(back.Script, example.Script), "foundryPolicy script round-trip")
		util.Assertf(EqualConstraints(example, back), "inconsistency in "+FoundryPolicyName)

		pref1, err := lib.ParsePrefixBytecode(example.Bytes())
		util.AssertNoError(err)
		pref2, err := lib.EvalFromSource(nil, "#"+FoundryPolicyName)
		util.AssertNoError(err)
		util.Assertf(bytes.Equal(pref1, pref2), "foundryPolicy prefix match")
	})
}
