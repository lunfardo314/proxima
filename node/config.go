package node

import (
	"os"
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
	pflag.String("logger.timelayout", "2006-01-02 15:04:05.000", "time format")
	pflag.String("logger.output", "stdout", "a list where to write log")

	pflag.String("multistate.name", "proximadb", "name of the multi-state database")
	pflag.String("txstore.type", "dummy", "one of: db | dummy | url")
	pflag.String("txstore.name", "", "depending on type: name of the db or url")
}

func initConfig(log *zap.SugaredLogger) {
	pflag.Parse()
	err := viper.BindPFlags(pflag.CommandLine)
	util.AssertNoError(err)

	viper.SetConfigName(".proxima")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	err = viper.ReadInConfig()
	util.AssertNoError(err)

	if viper.GetString("multistate.name") == "" {
		log.Errorf("multistate database not specified, cannot start the node")
		os.Exit(1)
	}
}

func newBootstrapLogger() *zap.SugaredLogger {
	return general.NewLogger("boot", zap.InfoLevel, []string{"stderr"}, "")
}

func newTopLogger() *zap.SugaredLogger {
	logLevel := zapcore.InfoLevel
	if viper.GetString("logger.level") == "debug" {
		logLevel = zapcore.DebugLevel
	}

	outputStr := viper.GetString("logger.output")
	outputs := strings.Split(outputStr, ",")
	if util.Find(outputs, "stdout") < 0 {
		outputs = append(outputs, "stdout")
	}

	return general.NewLogger("top", logLevel, outputs, viper.GetString("logger.timelayout"))
}
