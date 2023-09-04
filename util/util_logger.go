package util

import (
	"github.com/lunfardo314/unitrie/common"
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
	cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("06-01-02 15:04:05.000")
	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(lvl), zap.AddStacktrace(zapcore.FatalLevel))
	return log.Sugar().Named(name)
}
