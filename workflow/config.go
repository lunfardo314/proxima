package workflow

import (
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type (
	ConfigParams struct {
		topLogLevel      zapcore.Level
		consumerLogLevel map[string]zapcore.Level
		logOutput        []string
	}

	ConfigOption func(c *ConfigParams)
)

func defaultConfigParams() *ConfigParams {
	return &ConfigParams{
		topLogLevel:      zap.InfoLevel,
		consumerLogLevel: make(map[string]zapcore.Level),
		logOutput:        []string{"stdout"},
	}
}

func (c *ConfigParams) SetConsumerLogLevel(name string, lvl zapcore.Level) {
	c.consumerLogLevel[name] = lvl
}

func (c *ConfigParams) SetLogLevel(lvl zapcore.Level) {
	c.topLogLevel = lvl
}

func (c *ConfigParams) AddLogOutput(out string) {
	if util.Find(c.logOutput, out) == -1 {
		c.logOutput = append(c.logOutput, out)
	}
}

func LogLevel(lvl zapcore.Level) ConfigOption {
	return func(c *ConfigParams) {
		c.SetLogLevel(lvl)
	}
}

func ConsumerLogLevel(name string, lvl zapcore.Level) ConfigOption {
	return func(c *ConfigParams) {
		c.SetConsumerLogLevel(name, lvl)
	}
}

func LogOutput(out string) ConfigOption {
	return func(c *ConfigParams) {
		c.AddLogOutput(out)
	}
}

func GlobalConfigOptions(forceGlobalLevel ...zapcore.Level) ConfigOption {
	return func(c *ConfigParams) {
		if len(forceGlobalLevel) > 0 {
			c.SetLogLevel(forceGlobalLevel[0])
		} else {
			c.SetLogLevel(parseLevel("logger.level", zap.InfoLevel))
		}

		for _, n := range AllConsumerNames {
			if len(forceGlobalLevel) > 0 {
				c.SetLogLevel(forceGlobalLevel[0])
			} else {
				c.SetLogLevel(parseLevel("workflow."+n+".loglevel", zap.InfoLevel))
			}
		}
		c.AddLogOutput(viper.GetString("workflow.output"))
	}
}

func parseLevel(key string, def zapcore.Level) zapcore.Level {
	lvl, err := zapcore.ParseLevel(viper.GetString(key))
	if err != nil {
		return def
	}
	return lvl
}
