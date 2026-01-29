package ledger

import (
	"crypto/ed25519"
	"time"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/slicepool"
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

	Library struct {
		*easyfl.Library[*EvalContext]
		definitionsYAML    []byte
		constraintByPrefix map[string]*constraintRecord
		locksByName        map[string]LockParser
		upgradeChainData   *UpgradeChainData // Cached upgrade chain data, set when loaded from DB
		Constants                            // Embedded ledger constants for this library version
		txValidatorExpr    *easyfl.Expression[*EvalContext]
		TxValidator        func(ctx easyfl.GlobalData[*EvalContext], spool *slicepool.SlicePool) error
	}
)

func newLibrary(lib *easyfl.Library[*EvalContext], definitionsYAML []byte) *Library {
	ret := &Library{
		Library:            lib,
		definitionsYAML:    definitionsYAML,
		constraintByPrefix: make(map[string]*constraintRecord),
		locksByName:        make(map[string]LockParser),
	}
	return ret
}

func newBaseLibrary() *Library {
	return newLibrary(easyfl.NewBaseLibrary[*EvalContext](), nil)
}

// DefinitionsYAML returns the compiled library YAML definitions.
func (lib *Library) DefinitionsYAML() []byte {
	if len(lib.definitionsYAML) > 0 {
		return lib.definitionsYAML
	}
	return lib.Library.ToYAML(true, "# Proxima library upgraded from EasyFL base")
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

func GetTestingLedgerParams(seed ...int) (InitParameters, ed25519.PrivateKey) {
	s := 10000
	for _, i := range seed {
		s += i
	}

	pk := testutil.GetTestingPrivateKey(s)
	return DefaultParameters(pk, uint32(time.Now().Unix())), pk
}
