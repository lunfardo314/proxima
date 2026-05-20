package txbuildercore

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
//
// The embedded *engine.Library[any] is anonymous so engine methods
// (DecompileBytecode, FunctionCallPrefixByName, CompileLocalScript,
// …) are promoted directly onto *Library. CompileExpression is the
// one method shadowed by the wrapper below to trim the engine's
// 4-return form down to a wallet-friendly (bytecode, error).
type Library struct {
	*engine.Library[any]
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
	return &Library{Library: lib}, nil
}

// CompileExpression compiles an EasyFL source expression to bytecode.
// Shadows engine.Library.CompileExpression to drop the int / *Expression
// return values that wallet callers typically don't need. The full form
// is still reachable via l.Library.CompileExpression(...).
func (l *Library) CompileExpression(source string) ([]byte, error) {
	_, _, code, err := l.Library.CompileExpression(source)
	return code, err
}
