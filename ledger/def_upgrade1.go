package ledger

import (
	_ "embed"
	"encoding/json"
	"fmt"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/easyfl/engine"
)

// Library upgrade 1: mine transits are stamped no earlier than the settlement
// tick minus the sequencer pace, and the miner version moves to 5
// (kb/library_upgrade1.md). Pure EasyFL, no new embedded function, so it needs no
// entry in upgradeEmbeddedResolvers. Activated at slot 29000 and no longer listed in Upgrades:
// the library comes with the state; the builder stays for reference and tests.

//go:embed def/upgrade1.easyfl
var upgrade1Source string

// BuildUpgrade1Library applies upgrade 1 to the compiled JSON of the library
// before it and returns the compiled JSON of the upgraded one.
func BuildUpgrade1Library(prevJSON []byte) ([]byte, error) {
	lib, err := ParseLibraryFromJSON(prevJSON, GetEmbeddedFunctionResolver)
	if err != nil {
		return nil, fmt.Errorf("upgrade 1: parse the previous library: %w", err)
	}
	defs, err := upgradeDefinitionsJSON(lib, upgrade1Source)
	if err != nil {
		return nil, fmt.Errorf("upgrade 1: %w", err)
	}
	if err = easyfl.IntroduceUpdateJSONMulti(lib, GetEmbeddedFunctionResolver(lib), defs); err != nil {
		return nil, fmt.Errorf("upgrade 1: %w", err)
	}
	if err = lib.CommitUpdate(); err != nil {
		return nil, fmt.Errorf("upgrade 1: %w", err)
	}
	return easyfl.ToJSON(lib, true, false), nil
}

// upgradeDefinitionsJSON turns EasyFL source into the definitions of an upgrade:
// a function the library holds is replaced, with its number of arguments; any
// other is added, with the number of arguments its source uses. Replacing is
// only possible through JSON, and the source keeps the upgrade readable.
func upgradeDefinitionsJSON(lib *easyfl.Library[*EvalContext], source string) ([]byte, error) {
	parsed, err := engine.ParseFunctions(source)
	if err != nil {
		return nil, err
	}
	type definitions struct {
		Functions []engine.FuncDescriptorJSON `json:"functions"`
	}
	var ret definitions
	for _, f := range parsed {
		if f.IsVararg {
			return nil, fmt.Errorf("vararg function '%s' is not supported in an upgrade", f.Sym)
		}
		d := engine.FuncDescriptorJSON{Sym: f.Sym, Source: f.SourceCode, NumArgs: maxArgReference(f.SourceCode)}
		if existing, err := lib.FunctionByName(f.Sym); err == nil {
			d.Replace = true
			d.NumArgs = existing.NumParams
		}
		ret.Functions = append(ret.Functions, d)
	}
	return json.Marshal(&ret)
}

// maxArgReference is the number of arguments a function source uses: one more
// than its highest $n.
func maxArgReference(src string) int {
	n := 0
	for i := 0; i+1 < len(src); i++ {
		if src[i] == '$' && src[i+1] >= '0' && src[i+1] <= '9' {
			if k := int(src[i+1]-'0') + 1; k > n {
				n = k
			}
		}
	}
	return n
}
