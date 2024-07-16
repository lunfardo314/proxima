package workflow

import "go.uber.org/zap"

type (
	ConfigParams struct {
		doNotStartPruner      bool
		doNotStartSyncManager bool
	}

	ConfigOption func(c *ConfigParams)
)

func defaultConfigParams() ConfigParams {
	return ConfigParams{}
}

// OptionDoNotStartPruner used for testing, to disable pruner
// Config key: 'workflow.do_not_start_pruner: true'
func OptionDoNotStartPruner(c *ConfigParams) {
	c.doNotStartPruner = true
}

// OptionDoNotStartSyncManager used to disable sync manager which is optional if sync is not long
// Config key: 'workflow.do_not_start_sync_manager: true'
func OptionDoNotStartSyncManager(c *ConfigParams) {
	c.doNotStartSyncManager = true
}

func (cfg *ConfigParams) log(log *zap.SugaredLogger) {
	if cfg.doNotStartPruner {
		log.Info("[workflow config] do not start pruner")
	}
	if cfg.doNotStartSyncManager {
		log.Info("[workflow config] do not start sync manager")
	}
}
