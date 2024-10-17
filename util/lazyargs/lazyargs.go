package lazyargs

func Eval(args ...any) []any {
	ret := make([]any, len(args))
	for i, arg := range args {
		switch funArg := arg.(type) {
		case func() any:
			ret[i] = funArg()
		case func() string:
			ret[i] = funArg()
		case func() bool:
			ret[i] = funArg()
		case func() int:
			ret[i] = funArg()
		case func() byte:
			ret[i] = funArg()
		default:
			ret[i] = arg
		}
	}
	return ret
}
