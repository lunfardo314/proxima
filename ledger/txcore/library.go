package txcore

import (
	"github.com/lunfardo314/easyfl/engine"
)

// Library is the wallet-side compose surface over easyfl/engine.Library.
// The wallet builds one of these once at init (from parsed library
// descriptors received via host API or bundled at build time) and
// reuses it for every transaction it composes.
//
// The wallet does not register any constraint serdes here — bytecode
// emission is pure source compile, decoding is pure source decompile.
type Library struct {
	// Inner is the underlying engine library. Exposed so callers that
	// need the lower-level engine API (e.g. CompileLocalScript) can
	// reach it directly.
	Inner *engine.Library[any]
}

// NewLibrary constructs a Library from already-parsed descriptors.
// The wallet parses library.json in its own environment (its own JSON
// reader, host JSON via API, etc.) and hands the result here. No
// embed callback is supplied — the wallet does not run eval, so
// function bodies are unbound; only metadata (sym / funCode / arity)
// is needed for compile + decompile.
func NewLibrary(desc *engine.LibraryFromJSON) (*Library, error) {
	lib := engine.NewLibrary[any]()
	if err := lib.Upgrade(desc); err != nil {
		return nil, err
	}
	return &Library{Inner: lib}, nil
}

// CompileExpression compiles an EasyFL source expression to bytecode.
// The first return value is the bytecode; the int / signature returned
// by the engine method are dropped since wallets typically don't need
// them.
func (l *Library) CompileExpression(source string) ([]byte, error) {
	_, _, code, err := l.Inner.CompileExpression(source)
	return code, err
}

// DecompileBytecode turns bytecode back into an EasyFL source string.
// Used by wallet UIs that want to render "what does this UTXO do?"
// for an arbitrary constraint.
func (l *Library) DecompileBytecode(code []byte) (string, error) {
	return l.Inner.DecompileBytecode(code)
}

// FunctionCallPrefixByName returns the canonical call-prefix bytes
// for a function symbol with the given arity. Useful when a helper
// needs to embed a prefix literal (e.g. for parseBytecode-shaped
// constraint checks).
func (l *Library) FunctionCallPrefixByName(sym string, numArgs byte) ([]byte, error) {
	return l.Inner.FunctionCallPrefixByName(sym, numArgs)
}
