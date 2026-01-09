package ledger

import (
	"fmt"

	"github.com/lunfardo314/easyfl"
)

// TODO cleanup of the ledger definitions: remove unused function defs and optimize
// TODO revisit function naming convention

// This file contains all upgrade prescriptions to the base ledger provided by the EasyFL. It is "version 0" of the ledger.
// Ledger definition can be upgraded by adding new embedded and extended function with new binary codes.
// That will make ledger upgrades backwards compatible, because all past transactions and EasyFL constraint bytecodes
// outputs will be interpreted exactly the same way

func LibraryFromParameters(idParams InitParameters, verbose ...bool) *Library {
	ret := newBaseLibrary()
	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Base EasyFL library:\n")
		ret.PrintLibraryStats()
	}

	upgrade0(ret.Library, idParams)

	if len(verbose) > 0 && verbose[0] {
		fmt.Printf("------ Extended EasyFL library:\n")
		ret.PrintLibraryStats()
	}
	return ret
}

func LibraryYAMLFromParameters(id InitParameters, compiled bool) []byte {
	return LibraryFromParameters(id).ToYAML(compiled, "# Proxima ledger definitions")
}

func ParseLibraryFromYAML(
	yamlData []byte,
	getResolver ...func(lib *easyfl.Library[*EvalContext],
	) func(sym string) easyfl.EmbeddedFunction[*EvalContext]) (*easyfl.Library[*EvalContext], error) {
	lib, err := easyfl.NewLibraryFromYAML(yamlData, getResolver...)
	if err != nil {
		return nil, err
	}
	return lib, nil
}
