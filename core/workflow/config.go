package workflow

import (
	"go.uber.org/zap"
)

type (
	ConfigParams struct {
		disableMemDAGGC        bool
		enableSyncManager      bool
		maxConcurrentAttachers int
	}

	ConfigOption func(c *ConfigParams)
)

func defaultConfigParams() ConfigParams {
	return ConfigParams{
		maxConcurrentAttachers: DefaultMaxConcurrentAttachers,
	}
}

// OptionDisableMemDAGGC used for testing, to disable pruner
// Config key: 'workflow.do_not_start_pruner: true'
func OptionDisableMemDAGGC(c *ConfigParams) {
	c.disableMemDAGGC = true
}

// OptionEnableSyncManager used to disable sync manager which is optional if sync is not long
// Config key: 'workflow.do_not_start_sync_manager: true'
func OptionEnableSyncManager(c *ConfigParams) {
	c.enableSyncManager = true
}

// OptionMaxConcurrentAttachers overrides the default attacher limit. Used in tests.
func OptionMaxConcurrentAttachers(n int) ConfigOption {
	return func(c *ConfigParams) {
		c.maxConcurrentAttachers = n
	}
}

func (cfg *ConfigParams) log(log *zap.SugaredLogger) {
	if cfg.disableMemDAGGC {
		log.Info("[workflow config] do not start pruner")
	}
	if cfg.enableSyncManager {
		log.Info("[workflow config] start sync manager")
	}
}
