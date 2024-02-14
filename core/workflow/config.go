package workflow

import (
	"github.com/spf13/viper"
	"go.uber.org/zap/zapcore"
)

type (
	ConfigParams struct {
		doNotStartPruner bool
	}

	ConfigOption func(c *ConfigParams)
)

func defaultConfigParams() ConfigParams {
	return ConfigParams{}
}

func OptionDoNotStartPruner(c *ConfigParams) {
	c.doNotStartPruner = true
}

var allComponentNames = make([]string, 0)

func WithGlobalConfigOptions(c *ConfigParams) {
	//WithLogLevel(parseLevel("logger.level", zap.InfoLevel))(c)
	//
	//for _, n := range allComponentNames {
	//	WithConsumerLogLevel(n, parseLevel("workflow."+n+".loglevel", zap.InfoLevel))(c)
	//}
	//WithLogOutput(viper.GetString("logger.output"))(c)
	//WithLogTimeLayout(viper.GetString("logger.timelayout"))
}

func parseLevel(key string, def zapcore.Level) zapcore.Level {
	lvl, err := zapcore.ParseLevel(viper.GetString(key))
	if err != nil {
		return def
	}
	return lvl
}
