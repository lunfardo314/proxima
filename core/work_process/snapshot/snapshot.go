package snapshot

import (
	"io"
	"os"
	"path/filepath"
	"sort"
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
		keepLatest    int

		// metrics
		metricsEnabled bool
	}
)

const (
	Name = "snapshot"

	defaultSnapshotDirectory     = "snapshot"
	defaultSnapshotPeriodInSlots = 30
	defaultKeepLatest            = 3
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
	if ret.periodInSlots <= 0 {
		ret.periodInSlots = defaultSnapshotPeriodInSlots
	}

	ret.keepLatest = viper.GetInt("snapshot.keep_latest")
	if ret.keepLatest <= 0 {
		ret.keepLatest = defaultKeepLatest
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
	s.purgeOldSnapshots()

	period := time.Duration(s.periodInSlots) * ledger.L().ID.SlotDuration()
	s.Log().Infof("[snapshot] work process STARTED\n        target directory: %s\n        period: %v (%d slots)\n        : keep latest: %d",
		s.directory, period, s.periodInSlots, s.keepLatest)

	defer s.Log().Infof("[snapshot] work process STOPPED")

	for {
		select {
		case <-s.Ctx().Done():
			return
		case <-time.After(period):
			s.doSnapshot()
			s.purgeOldSnapshots()
		}
	}
}

func (s *Snapshot) doSnapshot() {
	start := time.Now()
	_, fname, err := multistate.SaveSnapshot(s.StateStore(), s.Ctx(), s.directory, io.Discard)
	if err != nil {
		s.Log().Errorf("[snapshot] failed to save snapshot: %v", err)
	} else {
		s.Log().Infof("[snapshot] snapshot has been saved to %s. It took %v", fname, time.Since(start))
	}
}

func (s *Snapshot) purgeOldSnapshots() {
	entries, err := os.ReadDir(s.directory)
	if err != nil {
		s.Log().Errorf("[snapshot] purgeOldSnapshots: %v", err)
		return
	}

	entries = util.PurgeSlice(entries, func(entry os.DirEntry) bool {
		// remain only regular files
		if _, err := entry.Info(); err != nil {
			return false
		}
		return entry.Type()&os.ModeType == 0
	})

	if len(entries) <= s.keepLatest {
		return
	}

	sort.Slice(entries, func(i, j int) bool {
		fii, _ := entries[i].Info()
		fij, _ := entries[j].Info()
		return fii.ModTime().Before(fij.ModTime())
	})

	for _, entry := range entries[:len(entries)-s.keepLatest] {
		fpath := filepath.Join(s.directory, entry.Name())
		if err = os.Remove(fpath); err != nil {
			s.Log().Errorf("[snapshot] failed to remove file %s", fpath)
		}
	}
}
