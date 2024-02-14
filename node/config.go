package node

import (
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

const (
	bootstrapLoggerName = "[boot]"
)

func newBootstrapLogger() *zap.SugaredLogger {
	return global.NewLogger(bootstrapLoggerName, zap.InfoLevel, []string{"stderr"}, "")
}

func newNodeLoggerFromConfig() *global.DefaultLogging {
	logLevel := zapcore.InfoLevel
	if viper.GetString("logger.level") == "debug" {
		logLevel = zapcore.DebugLevel
	}

	outputStr := viper.GetString("logger.output")
	outputs := strings.Split(outputStr, ",")
	if util.Find(outputs, "stdout") < 0 {
		outputs = append(outputs, "stdout")
	}

	return global.NewDefaultLogging("", logLevel, outputs)
}
