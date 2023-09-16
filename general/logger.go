package general

import (
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const defaultTimeLayout = "2006-01-02 15:04:05.000"

func NewLogger(name string, level zapcore.Level, outputs []string, timeLayout string) *zap.SugaredLogger {
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(level),
		Development:      true,
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      outputs,
		ErrorOutputPaths: outputs,
		DisableCaller:    true,
	}

	if timeLayout == "" {
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(defaultTimeLayout)
	} else {
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(timeLayout)
	}

	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(level), zap.AddStacktrace(zapcore.FatalLevel))

	return log.Sugar().Named(name)
}
