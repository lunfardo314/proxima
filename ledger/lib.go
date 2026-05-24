package ledger

import (
	"crypto/ed25519"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/slicepool"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/testutil"
)

type (
	// UpgradeChainData contains the upgrade UTXO chain data for a library.
	// This links each upgrade to its predecessor, forming a chain commitment.
	UpgradeChainData struct {
		UpgradeSlot     uint32   // The slot this library was upgraded at
		LibraryHash     [32]byte // Hash of this library
		PrevLibraryHash [32]byte // Hash of the previous library (BaseLibraryHash for slot 0)
		PrevUpgradeSlot uint32   // Slot of the previous upgrade (MaxSlot for slot 0)
	}

	IntegrityValidator func(ctx easyfl.GlobalData[*EvalContext], spool *slicepool.SlicePool) error
	Library            struct {
		*easyfl.Library[*EvalContext]
		*txbuildercore.Constants                                          // ledger constants for this library version (wallet-shared shape)
		definitionsJSON                        []byte
		constraintByPrefix                     map[string]*constraintRecord
		upgradeChainData                       *UpgradeChainData // Cached upgrade chain data, set when loaded from DB
		upgradeIndex                           uint16            // 0-based ordinal position in upgrade chain (genesis=0, first upgrade=1, etc.)
		// Names of the EasyFL functions implementing the tx integrity
		// validator (partial and full context). Used at lib load to
		// compile them into the pointers below. Server-only.
		TxIntegrityValidatorPartialContextName string
		TxIntegrityValidatorFullContextName    string
		TxIntegrityValidatorPartialContext     IntegrityValidator
		TxIntegrityValidatorFullContext        IntegrityValidator
		// precompiled expressions for optimization
		BranchInflationBonusBasePrecompiled atomic.Pointer[easyfl.Expression[*EvalContext]]
		BranchCoverageLowerBoundPrecompiled atomic.Pointer[easyfl.Expression[*EvalContext]]
		BranchCoverageUpperBoundPrecompiled atomic.Pointer[easyfl.Expression[*EvalContext]]
		HealthyCoverageDeltaPrecompiled     atomic.Pointer[easyfl.Expression[*EvalContext]]
		StorageDepositPrecompiled           atomic.Pointer[easyfl.Expression[*EvalContext]]
		// compiledScriptCache is the library-level cache of decoded local
		// scripts; populated by redeemScript, read by callRedeemer. See
		// local_script_cache.go.
		compiledScriptCache CompiledScriptCache
		scriptCacheOnce     sync.Once
	}
)

func newLibrary(lib *easyfl.Library[*EvalContext], definitionsJSON []byte) *Library {
	ret := &Library{
		Library:            lib,
		definitionsJSON:    definitionsJSON,
		constraintByPrefix: make(map[string]*constraintRecord),
	}
	return ret
}

func newBaseLibrary() *Library {
	return newLibrary(easyfl.NewBaseLibrary[*EvalContext](), nil)
}

// DefinitionsJSON returns the compiled library JSON definitions. The returned
// bytes are compact JSON (canonical for storage and on-the-wire); callers that
// want a human-readable form should re-serialize via easyfl.ToJSON(true, true).
func (lib *Library) DefinitionsJSON() []byte {
	if len(lib.definitionsJSON) > 0 {
		return lib.definitionsJSON
	}
	return easyfl.ToJSON(lib.Library, true, false)
}

// Decompile is the non-generic facade over engine.Library.DecompileBytecode
// (called without local-script substitution). Its signature matches the
// one on txbuildercore.Library so both library types satisfy the
// transaction.Decompiler interface used by the tx display path.
func (lib *Library) Decompile(code []byte) (string, error) {
	return lib.Library.DecompileBytecode(code)
}

// UpgradeChainData returns the upgrade chain data for this library.
// Returns nil if the library was not loaded from the DB (e.g., created in-memory for testing).
func (lib *Library) UpgradeChainData() *UpgradeChainData {
	return lib.upgradeChainData
}

// SetUpgradeChainData sets the upgrade chain data for this library.
// Called when the library is loaded from the DB.
func (lib *Library) SetUpgradeChainData(data *UpgradeChainData) {
	lib.upgradeChainData = data
}

// UpgradeIndex returns the 0-based ordinal position of this library in the upgrade chain.
// Genesis library = 0, first upgrade = 1, etc.
// This value is used as TxVersion in the transaction tuple.
func (lib *Library) UpgradeIndex() uint16 {
	return lib.upgradeIndex
}

// MustPreCompileTxIntegrityValidators sets tx layout validator for the initialized library
func (lib *Library) MustPreCompileTxIntegrityValidators() {
	if lib.TxIntegrityValidatorPartialContextName == "" {
		lib.TxIntegrityValidatorPartialContext = func(_ easyfl.GlobalData[*EvalContext], _ *slicepool.SlicePool) error {
			panic("tx integrity validator (partial context) has not beed initialized")
		}
		return
	}
	exprPartial, nargs, _, err := lib.CompileExpression(lib.TxIntegrityValidatorPartialContextName)
	util.AssertNoError(err)
	util.Assertf(nargs == 0, "transaction integrity validator (partial context) must be a closed EasyFL expression")

	lib.TxIntegrityValidatorPartialContext = func(ctx easyfl.GlobalData[*EvalContext], spool *slicepool.SlicePool) error {
		err1 := easyfl_util.CatchPanicOrError(func() error {
			res := easyfl.EvalExpressionWithSlicePool(ctx, spool, exprPartial)
			if len(res) == 0 {
				return fmt.Errorf("transaction integrity validation (partial context) failed")
			}
			return nil
		})
		return err1
	}

	if lib.TxIntegrityValidatorFullContextName == "" {
		lib.TxIntegrityValidatorFullContext = func(_ easyfl.GlobalData[*EvalContext], _ *slicepool.SlicePool) error {
			panic("tx integrity validator (full context) has not beed initialized")
		}
		return
	}
	exprFull, nargs, _, err := lib.CompileExpression(lib.TxIntegrityValidatorFullContextName)
	util.AssertNoError(err)
	util.Assertf(nargs == 0, "transaction integrity validator (full context) must be a closed EasyFL expression")

	lib.TxIntegrityValidatorFullContext = func(ctx easyfl.GlobalData[*EvalContext], spool *slicepool.SlicePool) error {
		err1 := easyfl_util.CatchPanicOrError(func() error {
			res := easyfl.EvalExpressionWithSlicePool(ctx, spool, exprFull)
			if len(res) == 0 {
				return fmt.Errorf("transaction integrity validation (full context) failed")
			}
			return nil
		})
		return err1
	}
}

func GetTestingLedgerParams(seed ...int) (InitParameters, ed25519.PrivateKey) {
	s := 10000
	for _, i := range seed {
		s += i
	}

	pk := testutil.GetTestingPrivateKey(s)
	par := DefaultParameters(pk, uint32(time.Now().Unix()))
	// Relax the on-chain healthiness check for tests: synthetic conflict /
	// short-past-cone branches typically have small coverageDelta. Setting
	// numerator=0 makes the predicate `0 < covDelta * den` — accepts any
	// positive coverageDelta, matching the relaxed-bounds convention used
	// elsewhere in test infrastructure (e.g. WithBranchCoverageBounds).
	par.HealthyCoverageNumerator = 0
	par.HealthyCoverageDenominator = 1
	return par, pk
}

func (lib *Library) mustCompile(src string, nArgs int) *easyfl.Expression[*EvalContext] {
	expr, na, _, err := lib.CompileExpression(src)
	util.AssertNoError(err)
	util.Assertf(nArgs == na, "expected %d arguments, got %d", nArgs, na)
	return expr
}
