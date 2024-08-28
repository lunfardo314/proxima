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

		directory     string
		periodInSlots int

		// metrics
		metricsEnabled bool
	}
)

const (
	Name = "snapshot"

	defaultSnapshotDirectory     = "snapshot"
	defaultSnapshotPeriodInSlots = 30
)

// TODO directory cleanup

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
	env.Log().Infof("[snapshot] directory is '%s'", ret.directory)
	util.Assertf(directoryExists(ret.directory), "snapshot directory '%s' is wrong or does not exist", ret.directory)

	ret.periodInSlots = viper.GetInt("snapshot.period_in_slots")
	if ret.periodInSlots <= defaultSnapshotPeriodInSlots {
		ret.periodInSlots = defaultSnapshotPeriodInSlots
	}
	ret.registerMetrics()
	go ret.snapshotLoop()

	return ret
}

func (s *Snapshot) registerMetrics() {
	// TODO implement snapshot metrics
}

func directoryExists(dir string) bool {
	fileInfo, err := os.Stat(dir)
	return err == nil && fileInfo.IsDir()
}

func (s *Snapshot) snapshotLoop() {
	s.Log().Infof("[snapshot] loop STARTED")
	defer s.Log().Infof("[snapshot] loop STOPPED")

	for {
		select {
		case <-s.Ctx().Done():
			return
		case <-time.After(time.Duration(s.periodInSlots) * ledger.L().ID.SlotDuration()):
			s.doSnapshot()
		}
	}
}

func (s *Snapshot) doSnapshot() {
	_, fname, err := multistate.SaveSnapshot(s.StateStore(), s.Ctx(), s.directory, io.Discard)
	if err != nil {
		s.Log().Errorf("[snapshot] failed to save snapshot: %v", err)
	} else {
		s.Log().Infof("[snapshot] snapshot has been saved to %s", fname)
	}
}
