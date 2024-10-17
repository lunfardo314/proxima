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
		case func() uint:
			ret[i] = funArg()
		case func() uint16:
			ret[i] = funArg()
		case func() uint32:
			ret[i] = funArg()
		case func() uint64:
			ret[i] = funArg()
		case func() int16:
			ret[i] = funArg()
		case func() int32:
			ret[i] = funArg()
		case func() int64:
			ret[i] = funArg()
		default:
			ret[i] = arg
		}
	}
	return ret
}
