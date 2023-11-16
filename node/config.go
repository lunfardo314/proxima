package node

import (
	"strings"

	"github.com/lunfardo314/proxima/general"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func init() {
	pflag.String("logger.level", "info", "log level")
	pflag.String("logger.timelayout", general.TimeLayoutDefault, "time format")
	pflag.String("logger.output", "stdout", "a list where to write log")

	pflag.String(general.ConfigKeyMultiStateDbName, "proximadb", "name of the multi-state database")
	pflag.String(general.ConfigKeyTxStoreType, "dummy", "one of: db | dummy | url")
	pflag.String(general.ConfigKeyTxStoreName, "", "depending on type: name of the db or url")

	pflag.Bool("pprof.enable", false, "enable pprof")
	pflag.Int("pprof.port", 8080, "default pprof port")
}

const (
	bootstrapLoggerName = "[boot]"
	nodeLoggerName      = "[node]"
)

func newBootstrapLogger() *zap.SugaredLogger {
	return general.NewLogger(bootstrapLoggerName, zap.InfoLevel, []string{"stderr"}, "")
}

func newNodeLoggerFromConfig() *zap.SugaredLogger {
	logLevel := zapcore.InfoLevel
	if viper.GetString("logger.level") == "debug" {
		logLevel = zapcore.DebugLevel
	}

	outputStr := viper.GetString("logger.output")
	outputs := strings.Split(outputStr, ",")
	if util.Find(outputs, "stdout") < 0 {
		outputs = append(outputs, "stdout")
	}

	return general.NewLogger(nodeLoggerName, logLevel, outputs, viper.GetString("logger.timelayout"))
}
