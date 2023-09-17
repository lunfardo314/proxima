package general

import (
	"github.com/lunfardo314/unitrie/common"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const TimeLayoutDefault = "01-02 15:04:05.000"

//const TimeLayoutDefault = "2006-01-02 15:04:05.000"

func NewLogger(name string, level zapcore.Level, outputs []string, timeLayout string) *zap.SugaredLogger {
	if len(outputs) == 0 {
		outputs = []string{"stdout"}
	}
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
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(TimeLayoutDefault)
	} else {
		cfg.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout(timeLayout)
	}

	log, err := cfg.Build()
	common.AssertNoError(err)
	log.Core()
	log = log.WithOptions(zap.IncreaseLevel(level), zap.AddStacktrace(zapcore.FatalLevel))

	return log.Sugar().Named(name)
}
