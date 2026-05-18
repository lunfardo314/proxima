package tests

import (
	"testing"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/stretchr/testify/require"
)

// Tests for easyfl upgrade features: forward references in Upgrade(), Clone() for
// safe upgrades, and recursion detection. These exercise the new multi-phase Upgrade()
// in the context of proxima's compiled ledger library.
//
// All tests clone the library before mutation to avoid polluting the global singleton.

// cloneLib returns a deep copy of the current ledger library for isolated testing.
func cloneLib() *easyfl.Library[*ledger.EvalContext] {
	return ledger.L(base.MaxSlot).Clone()
}

// TestUpgrade_ForwardReference verifies that an upgrade batch can contain forward
// references: funcCaller listed before funcCallee, where funcCaller calls funcCallee.
// This was not possible before the multi-phase Upgrade() refactor.
func TestUpgrade_ForwardReference(t *testing.T) {
	lib := cloneLib()

	// funcCaller is listed BEFORE funcCallee — a forward reference.
	// The new multi-phase Upgrade() resolves this because Phase 1 introduces
	// stubs for all functions before Phase 2 compiles any of them.
	jsonData := `{
  "functions": [
    {"sym": "testFwdCaller", "numArgs": 1, "source": "testFwdCallee($0)"},
    {"sym": "testFwdCallee", "numArgs": 1, "source": "len($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	// testFwdCaller(0x0102) should return len(0x0102) = 2
	lib.MustEqual("testFwdCaller(0x0102)", "uint8Bytes(2)")
	lib.MustEqual("testFwdCallee(0x0102)", "uint8Bytes(2)")
}

// TestUpgrade_ForwardReference_ThreeFunc verifies forward references across a chain
// of three new functions: A calls B, B calls C, all in a single batch with A listed first.
func TestUpgrade_ForwardReference_ThreeFunc(t *testing.T) {
	lib := cloneLib()

	jsonData := `{
  "functions": [
    {"sym": "testChainA", "numArgs": 1, "source": "testChainB($0)"},
    {"sym": "testChainB", "numArgs": 1, "source": "testChainC($0)"},
    {"sym": "testChainC", "numArgs": 1, "source": "len($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	lib.MustEqual("testChainA(0xaabbcc)", "uint8Bytes(3)")
}

// TestUpgrade_ForwardReference_Diamond verifies a diamond-shaped dependency graph
// (A->B, A->C, B->D, C->D) where functions appear in arbitrary order.
func TestUpgrade_ForwardReference_Diamond(t *testing.T) {
	lib := cloneLib()

	// A calls both B and C; B and C both call D. Listed in reverse dependency order.
	jsonData := `{
  "functions": [
    {"sym": "testDiamA", "numArgs": 1, "source": "add(testDiamB($0), testDiamC($0))"},
    {"sym": "testDiamB", "numArgs": 1, "source": "testDiamD($0)"},
    {"sym": "testDiamC", "numArgs": 1, "source": "testDiamD($0)"},
    {"sym": "testDiamD", "numArgs": 1, "source": "byte($0, 0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	// byte(0x0507, 0) = 5, so B and C each return 5, A returns add(5, 5) = 10
	lib.MustEqual("testDiamD(0x0507)", "5")
	lib.MustEqual("testDiamA(0x0507)", "uint8Bytes(10)")
}

// TestUpgrade_RecursionDetection_Self verifies that a function calling itself
// is rejected by the cycle detection in Phase 3.
func TestUpgrade_RecursionDetection_Self(t *testing.T) {
	lib := cloneLib()

	jsonData := `{
  "functions": [
    {"sym": "testSelfRec", "numArgs": 1, "source": "testSelfRec($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recursion detected")
}

// TestUpgrade_RecursionDetection_Mutual verifies that mutual recursion (A->B, B->A)
// is detected and rejected.
func TestUpgrade_RecursionDetection_Mutual(t *testing.T) {
	lib := cloneLib()

	jsonData := `{
  "functions": [
    {"sym": "testMutA", "numArgs": 1, "source": "testMutB($0)"},
    {"sym": "testMutB", "numArgs": 1, "source": "testMutA($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recursion detected")
}

// TestUpgrade_RecursionDetection_Indirect verifies that indirect recursion
// (A->B->C->A) is detected across three functions.
func TestUpgrade_RecursionDetection_Indirect(t *testing.T) {
	lib := cloneLib()

	jsonData := `{
  "functions": [
    {"sym": "testIndA", "numArgs": 1, "source": "testIndB($0)"},
    {"sym": "testIndB", "numArgs": 1, "source": "testIndC($0)"},
    {"sym": "testIndC", "numArgs": 1, "source": "testIndA($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recursion detected")
}

// TestUpgrade_CloneSafeUpgrade verifies the Clone()-based safe upgrade pattern:
// clone the library, attempt upgrade on the clone, and verify the original
// is completely unaffected regardless of whether the upgrade succeeds or fails.
func TestUpgrade_CloneSafeUpgrade(t *testing.T) {
	origLib := ledger.L(base.MaxSlot)
	origHash := origLib.LibraryHash()
	origNumFuncs := origLib.NumFunctions()

	t.Run("clone_success", func(t *testing.T) {
		// Clone and perform a valid upgrade on the clone
		clone := origLib.Clone()

		jsonData := `{
  "functions": [
    {"sym": "testCloneFunc", "numArgs": 2, "source": "add($0, $1)"}
  ]
}`
		fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
		require.NoError(t, err)

		err = clone.Upgrade(fromJSON)
		require.NoError(t, err)

		// Clone has the new function
		require.NotEqual(t, origHash, clone.LibraryHash())
		require.Equal(t, origNumFuncs+1, clone.NumFunctions())
		clone.MustEqual("testCloneFunc(3, 5)", "uint8Bytes(8)")

		// Original is unchanged
		require.Equal(t, origHash, origLib.LibraryHash())
		require.Equal(t, origNumFuncs, origLib.NumFunctions())
	})

	t.Run("clone_failure_discard", func(t *testing.T) {
		// Clone and attempt an invalid upgrade (recursion)
		clone := origLib.Clone()

		jsonBad := `{
  "functions": [
    {"sym": "testBadA", "numArgs": 1, "source": "testBadB($0)"},
    {"sym": "testBadB", "numArgs": 1, "source": "testBadA($0)"}
  ]
}`
		fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonBad))
		require.NoError(t, err)

		err = clone.Upgrade(fromJSON)
		require.Error(t, err) // cycle detected

		// Original is still completely intact
		require.Equal(t, origHash, origLib.LibraryHash())
		require.Equal(t, origNumFuncs, origLib.NumFunctions())
	})
}

// TestUpgrade_CloneHashIntegrity verifies that Clone() produces a library whose
// hash matches the original, as guaranteed by the deep-copy sanity check.
func TestUpgrade_CloneHashIntegrity(t *testing.T) {
	lib := ledger.L(base.MaxSlot)

	clone := lib.Clone()

	require.Equal(t, lib.LibraryHash(), clone.LibraryHash())
	require.Equal(t, lib.NumFunctions(), clone.NumFunctions())
}

// TestUpgrade_BackwardCompatible verifies that sequential dependency patterns
// (funcX uses base, funcY uses funcX -- no forward refs) still work correctly
// with the new multi-phase Upgrade(). This is the pre-existing pattern used
// throughout proxima's ledger definitions.
func TestUpgrade_BackwardCompatible(t *testing.T) {
	lib := cloneLib()

	// Sequential ordering: testSeqX defined before testSeqY which uses it
	jsonData := `{
  "functions": [
    {"sym": "testSeqX", "numArgs": 2, "source": "add($0, $1)"},
    {"sym": "testSeqY", "numArgs": 2, "source": "testSeqX($0, $1)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	lib.MustEqual("testSeqX(3, 5)", "uint8Bytes(8)")
	lib.MustEqual("testSeqY(3, 5)", "uint8Bytes(8)")
}

// TestUpgrade_ReplaceWithForwardRef verifies that a replaced function can call
// a new function added in the same batch, exercising the replace + forward ref
// combination that was impossible before the multi-phase refactor.
func TestUpgrade_ReplaceWithForwardRef(t *testing.T) {
	lib := cloneLib()

	// Step 1: add a function to later replace
	json1 := `{
  "functions": [
    {"sym": "testReplTarget", "numArgs": 2, "source": "add($0, $1)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(json1))
	require.NoError(t, err)
	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)
	lib.MustEqual("testReplTarget(3, 5)", "uint8Bytes(8)")

	// Step 2: replace testReplTarget to call a new testReplHelper (forward ref)
	json2 := `{
  "functions": [
    {"sym": "testReplTarget", "numArgs": 2, "replace": true, "source": "testReplHelper($0, $1)"},
    {"sym": "testReplHelper", "numArgs": 2, "source": "mul($0, $1)"}
  ]
}`
	fromJSON, err = easyfl.ReadLibraryFromJSON([]byte(json2))
	require.NoError(t, err)
	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	// testReplTarget now delegates to testReplHelper (mul), so 3*5=15
	lib.MustEqual("testReplTarget(3, 5)", "uint8Bytes(15)")
	lib.MustEqual("testReplHelper(3, 5)", "uint8Bytes(15)")
}

// TestUpgrade_ReplaceInducedCycle verifies that replacing a function to create
// a cycle with an existing function is detected. If existing A calls B, and
// we replace B to call A, the cycle A->B->A must be caught.
func TestUpgrade_ReplaceInducedCycle(t *testing.T) {
	lib := cloneLib()

	// Step 1: add depB, then depA that calls depB
	json1 := `{
  "functions": [
    {"sym": "testDepB", "numArgs": 1, "source": "byte($0, 0)"},
    {"sym": "testDepA", "numArgs": 1, "source": "testDepB($0)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(json1))
	require.NoError(t, err)
	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	// Step 2: replace depB to call depA — creates cycle depA->depB->depA
	json2 := `{
  "functions": [
    {"sym": "testDepB", "numArgs": 1, "replace": true, "source": "testDepA($0)"}
  ]
}`
	fromJSON, err = easyfl.ReadLibraryFromJSON([]byte(json2))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.Error(t, err)
	require.Contains(t, err.Error(), "recursion detected")
}

// TestUpgrade_ForwardRefUsingExistingLedgerFunctions verifies that new functions
// added via forward references can call existing proxima ledger functions (like
// len, add, concat) alongside each other.
func TestUpgrade_ForwardRefUsingExistingLedgerFunctions(t *testing.T) {
	lib := cloneLib()

	// testCombA calls testCombB, testCombB uses the existing concat function,
	// testCombA listed first (forward ref to testCombB)
	jsonData := `{
  "functions": [
    {"sym": "testCombA", "numArgs": 2, "source": "len(testCombB($0, $1))"},
    {"sym": "testCombB", "numArgs": 2, "source": "concat($0, $1)"}
  ]
}`
	fromJSON, err := easyfl.ReadLibraryFromJSON([]byte(jsonData))
	require.NoError(t, err)

	err = lib.Upgrade(fromJSON)
	require.NoError(t, err)

	// concat(0x01, 0x02) = 0x0102, len(0x0102) = 2
	lib.MustEqual("testCombA(1, 2)", "uint8Bytes(2)")
}
