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

// LibraryJSONFromParameters builds the library from InitParameters and serializes
// it to JSON. `compiled=true` includes funCodes, bytecodes, and the top-level hash.
// The output is indented for human readability; storage paths use ToJSON(true, false).
func LibraryJSONFromParameters(id InitParameters, compiled bool) []byte {
	return easyfl.ToJSON(LibraryFromParameters(id).Library, compiled, true)
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
