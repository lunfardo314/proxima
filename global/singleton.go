package global

var globalLogger = NewDefault()

// SetGlobalLogger not thread safe
func SetGlobalLogger(l *Global) {
	globalLogger = l
}
