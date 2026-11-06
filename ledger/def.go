package ledger

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
)

// TODO cleanup of the ledger definitions: remove unused function defs and optimize
// TODO revisit function naming convention

// This file contains all upgrade prescriptions to the base ledger provided by the EasyFL. It is "version 0" of the ledger.
// Ledger definition can be upgraded by adding new embedded and extended function with new binary codes.
// That will make ledger upgrades backwards compatible, because all past transactions and EasyFL constraint bytecodes
// outputs will be interpreted exactly the same way

// LibraryFromParameters builds the library of upgrade 0: the base EasyFL library with the
// ledger definitions and the constants of the parameters. The genesis library of a new ledger
// also folds the upgrades of idParams.FoldUpgrades: LibraryJSONFromParameters.
func LibraryFromParameters(idParams InitParameters, verbose ...bool) *Library {
	ret := newBaseLibrary()
	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Base EasyFL library:\n")
		ret.PrintLibraryStats()
	}

	upgrade0(ret.Library, ConstantsJSONFromParamsUpgrade0(idParams))

	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Extended EasyFL library:\n")
		ret.PrintLibraryStats()
	}
	return ret
}

// LibraryJSONFromParameters is the genesis library of a new ledger as JSON: upgrade 0 from the
// parameters with the upgrades of id.FoldUpgrades built on it (FoldUpgrades), so a new ledger
// starts with the current definitions. `compiled=true` includes funCodes, bytecodes, and the
// top-level hash. The output is indented for human readability; storage paths use ToJSON(true, false).
func LibraryJSONFromParameters(id InitParameters, compiled bool) []byte {
	lib := LibraryFromParameters(id).Library
	if len(id.FoldUpgrades) == 0 {
		return easyfl.ToJSON(lib, compiled, true)
	}
	folded, err := FoldUpgrades(easyfl.ToJSON(lib, true, false), id.FoldUpgrades)
	util.AssertNoError(err)
	lib, err = ParseLibraryFromJSON(folded, GetEmbeddedFunctionResolver)
	util.AssertNoError(err)
	return easyfl.ToJSON(lib, compiled, true)
}

// RebuildGenesisLibraryJSON builds again, from the base EasyFL library, the stored genesis
// library of a ledger: upgrade 0 with the genesis constants taken from the stored library,
// then the first FoldedUpgrades of upgrades. Its hash equals the stored one when this release
// still holds the definitions the ledger was created with.
func RebuildGenesisLibraryJSON(storedJSON []byte, upgrades []*UpgradeDefinition) ([]byte, error) {
	stored, err := ParseLibraryFromJSON(storedJSON, GetEmbeddedFunctionResolver)
	if err != nil {
		return nil, fmt.Errorf("parse the stored genesis library: %w", err)
	}
	n := FoldedUpgrades(stored)
	if n > len(upgrades) {
		return nil, fmt.Errorf("the genesis library folds %d upgrades, this release lists %d", n, len(upgrades))
	}
	constantsJSON, err := genesisConstantsJSON(storedJSON)
	if err != nil {
		return nil, err
	}
	lib := newBaseLibrary()
	err = util.CatchPanicOrError(func() error {
		upgrade0(lib.Library, constantsJSON)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("rebuild upgrade 0: %w", err)
	}
	return FoldUpgrades(easyfl.ToJSON(lib.Library, true, false), upgrades[:n])
}

// genesisConstantsJSON is the constants template of upgrade 0 with every value set by the
// parameters replaced by the source the stored genesis library holds for that constant.
func genesisConstantsJSON(storedJSON []byte) ([]byte, error) {
	var stored struct {
		Functions []struct {
			Sym    string `json:"sym"`
			Source string `json:"source"`
		} `json:"functions"`
	}
	if err := json.Unmarshal(storedJSON, &stored); err != nil {
		return nil, fmt.Errorf("parse the stored genesis library: %w", err)
	}
	sources := make(map[string]string, len(stored.Functions))
	for _, f := range stored.Functions {
		sources[f.Sym] = f.Source
	}
	var tmpl map[string]any
	if err := json.Unmarshal([]byte(_definitionsLedgerConstantsTemplateUpgrade0), &tmpl); err != nil {
		return nil, fmt.Errorf("parse the constants template: %w", err)
	}
	functions, _ := tmpl["functions"].([]any)
	for _, f := range functions {
		fd, _ := f.(map[string]any)
		src, _ := fd["source"].(string)
		if !strings.Contains(src, "{{") {
			continue
		}
		sym, _ := fd["sym"].(string)
		storedSrc, found := sources[sym]
		if !found {
			return nil, fmt.Errorf("the stored genesis library has no constant '%s'", sym)
		}
		fd["source"] = storedSrc
	}
	return json.Marshal(tmpl)
}

func ParseLibraryFromJSON(
	jsonData []byte,
	getResolver ...func(lib *easyfl.Library[*EvalContext],
	) func(sym string) easyfl.EmbeddedFunction[*EvalContext]) (*easyfl.Library[*EvalContext], error) {
	lib, err := easyfl.NewLibraryFromJSON(jsonData, getResolver...)
	if err != nil {
		return nil, err
	}
	return lib, nil
}
