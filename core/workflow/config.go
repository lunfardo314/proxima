package workflow

import "go.uber.org/zap"

type (
	ConfigParams struct {
		doNotStartPruner  bool
		enableSyncManager bool
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

// OptionEnableSyncManager used to disable sync manager which is optional if sync is not long
// Config key: 'workflow.do_not_start_sync_manager: true'
func OptionEnableSyncManager(c *ConfigParams) {
	c.enableSyncManager = true
}

func (cfg *ConfigParams) log(log *zap.SugaredLogger) {
	if cfg.doNotStartPruner {
		log.Info("[workflow config] do not start pruner")
	}
	if cfg.enableSyncManager {
		log.Info("[workflow config] start sync manager")
	}
}
