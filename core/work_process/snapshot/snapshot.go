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
