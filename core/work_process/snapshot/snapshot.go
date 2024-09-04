package snapshot

import (
	"io"
	"os"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/spf13/viper"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
	}

	Snapshot struct {
		environment
		directory  string
		keepLatest int
	}
)

const (
	Name = "snapshot"

	defaultSnapshotDirectory     = "snapshot"
	defaultSnapshotPeriodInSlots = 30
	defaultKeepLatest            = 3
)

func Start(env environment) *Snapshot {
	ret := &Snapshot{
		environment: env,
	}
	if !viper.GetBool("snapshot.enable") {
		// will not have any effect
		env.Log().Infof("[snapshot] is disabled")
		return ret
	}
	env.Log().Infof("[snapshot] is enabled")

	ret.directory = viper.GetString("snapshot.directory")
	if ret.directory == "" {
		ret.directory = defaultSnapshotDirectory
	}
	env.Log().Infof("%s directory is '%s'", Name, ret.directory)
	util.Assertf(directoryExists(ret.directory), "snapshot directory '%s' is wrong or does not exist", ret.directory)

	periodInSlots := viper.GetInt("snapshot.period_in_slots")
	if periodInSlots <= 0 {
		periodInSlots = defaultSnapshotPeriodInSlots
	}
	period := time.Duration(periodInSlots) * ledger.L().ID.SlotDuration()

	ret.keepLatest = viper.GetInt("snapshot.keep_latest")
	if ret.keepLatest <= 0 {
		ret.keepLatest = defaultKeepLatest
	}

	ret.registerMetrics()

	env.RepeatInBackground(Name, period, func() bool {
		ret.doSnapshot()
		ret.purgeOldSnapshots()
		return true
	}, true)

	ret.Log().Infof("[snapshot] work process STARTED\n        target directory: %s\n        period: %v (%d slots)\n        : keep latest: %d",
		ret.directory, period, periodInSlots, ret.keepLatest)

	return ret
}

func (s *Snapshot) registerMetrics() {
	// TODO implement snapshot metrics
}

func directoryExists(dir string) bool {
	fileInfo, err := os.Stat(dir)
	return err == nil && fileInfo.IsDir()
}

func (s *Snapshot) doSnapshot() {
	_, fname, stats, err := multistate.SaveSnapshot(s.StateStore(), s.Ctx(), s.directory, io.Discard)
	if err != nil {
		s.Log().Errorf("[snapshot] failed to save snapshot: %v", err)
	} else {
		s.Log().Infof("[snapshot] snapshot has been saved to %s.\n%s", fname, stats.Lines("             ").String())
	}
}

func (s *Snapshot) purgeOldSnapshots() {
	err := util.PurgeFilesInDirectory(s.directory, "*.snapshot", s.keepLatest)
	if err != nil {
		s.Log().Errorf("[snapshot] purgeOldSnapshots: %v", err)
		return
	}
}
