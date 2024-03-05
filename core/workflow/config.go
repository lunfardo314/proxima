package workflow

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
