package node

import (
	"strings"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func init() {
	pflag.String("logger.level", "info", "log level")
	pflag.String("logger.timelayout", global.TimeLayoutDefault, "time format")
	pflag.String("logger.output", "stdout", "a list where to write log")

	pflag.Bool("pprof.enable", false, "enable pprof")
	pflag.Int("pprof.port", 8080, "default pprof port")
}

const (
	bootstrapLoggerName = "[boot]"
	nodeLoggerName      = "[node]"
)

func newBootstrapLogger() *zap.SugaredLogger {
	return global.NewLogger(bootstrapLoggerName, zap.InfoLevel, []string{"stderr"}, "")
}

func newNodeLoggerFromConfig() (*zap.SugaredLogger, []string) {
	logLevel := zapcore.InfoLevel
	if viper.GetString("logger.level") == "debug" {
		logLevel = zapcore.DebugLevel
	}

	outputStr := viper.GetString("logger.output")
	outputs := strings.Split(outputStr, ",")
	if util.Find(outputs, "stdout") < 0 {
		outputs = append(outputs, "stdout")
	}

	return global.NewLogger(nodeLoggerName, logLevel, outputs, viper.GetString("logger.timelayout")), outputs
}
