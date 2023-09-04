package testutil

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func NewNamedLogger(name string, logLevel ...zapcore.Level) *zap.SugaredLogger {
	lvl := zap.InfoLevel
	if len(logLevel) > 0 {
		lvl = logLevel[0]
	}
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(lvl),
		Development:      true,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
		DisableCaller:    true,
	}
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("04:05.00000")
	log, err := cfg.Build()
	//log, err := cfg.Build(zap.WrapCore(func(core zapcore.Core) zapcore.Core {
	//	return zapcore.RegisterHooks(core, func(entry zapcore.Entry) error {
	//		//entry.Time = time.Time{}
	//		//fmt.Printf("----------- hook called : %v\n", entry.Time)
	//		return nil
	//	})
	//}))
	if err != nil {
		panic(err)
	}
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(lvl), zap.AddStacktrace(zapcore.FatalLevel))
	return log.Sugar().Named(name)
}
