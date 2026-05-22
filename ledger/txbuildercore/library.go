package txbuildercore

import (
	"sync"

	"github.com/lunfardo314/easyfl/engine"
)

// Library is the compose / decompile surface over easyfl/engine.Library.
// The wallet builds one of these once at init (from parsed library
// descriptors received via host API or bundled at build time) and
// reuses it for every transaction it composes.
//
// The type is generic over T because the underlying engine.Library is
// generic over its evaluation-context type. The wallet uses T=any
// (engine.Library[any]) — it never runs scripts. The full ledger uses
// T=ledger.EvalContext for full constraint evaluation; for display
// purposes a server-side caller can wrap its existing engine library
// as &Library[*EvalContext]{Library: ledgerLib.Library} — a zero-cost
// view, no JSON re-parse. Compose helpers below are bound to
// *Library[any] because they are wallet-side; the embedded
// engine.Library methods that do not depend on T (ParseBytecodeOneLevel,
// DecompileBytecode when called without local-script substitution) are
// usable on both instantiations and underpin the Decompiler interface
// that bridges server and wallet display paths.
//
// CompileExpression is shadowed below to trim the engine's 4-return
// form down to a wallet-friendly (bytecode, error).
type Library[T any] struct {
	*engine.Library[T]
	// lockCache memoises canonical lock bytecodes per-Library; one
	// instance shared across goroutines via mu. The cached bytecode is
	// a per-kind constant (sigLock, tagAlong, chainLock, …).
	lockCacheMu sync.Mutex
	lockCache   map[string][]byte
}

// NewLibrary constructs the wallet-default Library (T=any) from already-
// parsed descriptors. The wallet parses library.json in its own
// environment (its own JSON reader, host JSON via API, etc.) and hands
// the result here. No embed callback is supplied — the wallet does not
// run eval, so function bodies are unbound; only metadata (sym /
// funCode / arity) is needed for compile + decompile. Server code
// constructs Library[*EvalContext] directly as a struct literal over
// its existing engine library.
func NewLibrary(desc *engine.LibraryFromJSON) (*Library[any], error) {
	lib := engine.NewLibrary[any]()
	if err := lib.Upgrade(desc); err != nil {
		return nil, err
	}
	return &Library[any]{Library: lib}, nil
}

// CompileExpression compiles an EasyFL source expression to bytecode.
// Shadows engine.Library.CompileExpression to drop the int / *Expression
// return values that wallet callers typically don't need. The full form
// is still reachable via l.Library.CompileExpression(...).
func (l *Library[T]) CompileExpression(source string) ([]byte, error) {
	_, _, code, err := l.Library.CompileExpression(source)
	return code, err
}

// Decompile is a non-generic facade over engine.Library.DecompileBytecode
// (called without local-script substitution). Its non-generic
// signature lets *Library[any] and *Library[*EvalContext] both bind to
// a single transaction.Decompiler interface used by the tx display
// path.
func (l *Library[T]) Decompile(code []byte) (string, error) {
	return l.Library.DecompileBytecode(code)
}
