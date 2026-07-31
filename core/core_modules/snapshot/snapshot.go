package snapshot

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.Store
		GetOwnSequencerID() *base.ChainID
		IsSynced() bool
	}

	Snapshot struct {
		environment
		directory     string
		keepLatest    int
		safeSlotsBack int
	}
)

const (
	Name = "snapshot"

	defaultSnapshotDirectory     = "."
	defaultSnapshotPeriodInSlots = 176 // ~30 minutes at 10.24 sec/slot
	defaultKeepLatest            = 3
	defaultSafetySlots           = 20
)

func Start(env environment) {
	ret := &Snapshot{
		environment: env,
	}
	if !viper.GetBool("snapshot.enable") {
		// will not have any effect
		env.Log().Infof("[snapshot] is disabled")
		return
	}
	env.Log().Infof("[snapshot] is enabled")

	ret.directory = SnapshotDirectory()
	env.Log().Infof("%s directory is '%s'", Name, ret.directory)
	if err := checkSnapshotDirectory(ret.directory); err != nil {
		env.Log().Errorf("[snapshot] snapshotting is DISABLED: %v", err)
		return
	}

	periodInSlots := viper.GetInt("snapshot.period_in_slots")
	if periodInSlots <= 0 {
		periodInSlots = defaultSnapshotPeriodInSlots
	}
	period := time.Duration(periodInSlots) * ledger.L(0).SlotDuration()

	ret.keepLatest = viper.GetInt("snapshot.keep_latest")
	if ret.keepLatest <= 0 {
		ret.keepLatest = defaultKeepLatest
	}

	ret.safeSlotsBack = viper.GetInt("snapshot.safety_slots")
	if ret.safeSlotsBack == 0 {
		ret.safeSlotsBack = defaultSafetySlots
	}

	ret.registerMetrics()

	// randomize initial delay to minimize snapshot overlap between nodes
	initialDelay := time.Duration(rand.Int63n(int64(period)))

	ln := lines.New("          ").
		Add("target directory: %s", ret.directory).
		Add("frequency: %v (%d slots)", period, periodInSlots).
		Add("keep latest: %d", ret.keepLatest).
		Add("safety slot back: %d", ret.safeSlotsBack).
		Add("initial delay: %v", initialDelay)
	ret.Log().Infof("[snapshot] work process STARTED\n%s", ln.String())

	env.MarkWorkProcessStarted(Name)
	go func() {
		defer env.MarkWorkProcessStopped(Name)

		// wait random initial delay (staggers snapshots across nodes)
		select {
		case <-env.Ctx().Done():
			return
		case <-time.After(initialDelay):
		}
		// always skip the first period after startup: a freshly started or
		// snapshot-restored node should not immediately take a snapshot even if
		// the schedule says it is due. RepeatSync waits one full period before
		// its first call, so the first snapshot fires at initialDelay + period.
		env.RepeatSync(period, func() bool {
			ret.doSnapshot()
			ret.purgeOldSnapshots()
			return true
		})
	}()
	return
}

// SnapshotDirectory returns the configured snapshot directory from snapshot.directory config.
// Default is "." (current working directory). This is the single authoritative location
// for snapshot files, used by both snapshot creation and snapshot_restore.
func SnapshotDirectory() string {
	dir := viper.GetString("snapshot.directory")
	if dir == "" {
		dir = defaultSnapshotDirectory
	}
	return dir
}

func (s *Snapshot) registerMetrics() {
	// TODO implement snapshot metrics
}

// checkSnapshotDirectory validates the configured snapshot directory: it must exist,
// be a directory, and be writable. Returns a descriptive error if any check fails.
func checkSnapshotDirectory(dir string) error {
	fileInfo, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("snapshot directory '%s' does not exist", dir)
		}
		return fmt.Errorf("cannot access snapshot directory '%s': %w", dir, err)
	}
	if !fileInfo.IsDir() {
		return fmt.Errorf("snapshot path '%s' is not a directory", dir)
	}
	// verify write access by creating and removing a temporary file
	tmp, err := os.CreateTemp(dir, ".snapshot_write_test")
	if err != nil {
		return fmt.Errorf("snapshot directory '%s' is not writable: %w", dir, err)
	}
	_ = tmp.Close()
	_ = os.Remove(tmp.Name())
	return nil
}

func (s *Snapshot) doSnapshot() {
	if !s.IsSynced() {
		s.Log().Infof("[snapshot] not synced, skipping snapshot")
		return
	}
	snapshotBranch := multistate.FindLatestReliableBranchAndNSlotsBack(s.StateStore(), s.safeSlotsBack)
	if snapshotBranch == nil {
		s.Log().Errorf("[snapshot] can't find latest reliable branch")
		return
	}
	s.SetSnapshotting(true)
	fname, stats, err := multistate.SaveSnapshot(s.StateStore(), snapshotBranch, s.Ctx(), s.directory, io.Discard)
	s.SetSnapshotting(false)
	if err != nil {
		s.Log().Errorf("[snapshot] failed to save snapshot: %v", err)
	} else {
		s.Log().Infof("[snapshot] snapshot has been saved to %s.\n%s\nBranch data:\n%s",
			fname, stats.Lines("             ").String(), snapshotBranch.Lines("             ").String())
	}
}

func (s *Snapshot) purgeOldSnapshots() {
	err := util.PurgeFilesInDirectory(s.directory, "*.snapshot", s.keepLatest)
	if err != nil {
		s.Log().Errorf("[snapshot] purgeOldSnapshots: %v", err)
		return
	}
}
