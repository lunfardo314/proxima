package ledger

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
)

// Predefined foundry policy scripts. Each function returns compiled
// EasyFL bytecode suitable for attachment at ConstraintIndexFoundryPolicy
// (= 5) on a foundry output. The script bodies (in
// ledger/def/native_token.easyfl) AND each policy-specific check with
// `selfImmutableOnSuccessorIndex(foundryPolicyConstraintIndex)`, so the
// policy bytes self-lock across every chain transit.

const (
	FoundryNonDestructibleName = "foundryNonDestructible"
	FoundryMaxSupplyName       = "foundryMaxSupply"
)

// FoundryNonDestructibleBytecode returns the bytecode for the
// "non-destructible" policy: the foundry chain can be discontinued only
// when the consumed foundry's supply is 0 (all minted tokens must be
// burned back into the foundry before retirement).
func FoundryNonDestructibleBytecode() []byte {
	return mustBinFromSource(FoundryNonDestructibleName)
}

// FoundryMaxSupplyBytecode returns the bytecode for the "max-supply"
// policy with cap maxSupply: on every transit (including origin) the
// produced foundry's supply must be <= maxSupply.
func FoundryMaxSupplyBytecode(maxSupply uint64) []byte {
	return mustBinFromSource(fmt.Sprintf("%s(u64/%d)", FoundryMaxSupplyName, maxSupply))
}

func init() {
	// Smoke test: both predefined policy bytecodes must compile cleanly
	// at library init time. A regression here would surface during ledger
	// startup rather than only on first `foundry create` invocation.
	registerInlineTest(func(lib *Library) {
		nd := FoundryNonDestructibleBytecode()
		util.Assertf(len(nd) > 0, "FoundryNonDestructibleBytecode produced empty bytecode")
		_, err := lib.ParsePrefixBytecode(nd)
		util.AssertNoError(err)

		ms := FoundryMaxSupplyBytecode(1_000_000)
		util.Assertf(len(ms) > 0, "FoundryMaxSupplyBytecode produced empty bytecode")
		_, err = lib.ParsePrefixBytecode(ms)
		util.AssertNoError(err)
	})
}
