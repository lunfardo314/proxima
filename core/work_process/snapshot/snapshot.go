package snapshot

import (
	"io"
	"os"
	"time"

	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
	"github.com/spf13/viper"
)

type (
	environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
		GetOwnSequencerID() *ledger.ChainID
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

	defaultSnapshotDirectory     = "snapshot"
	defaultSnapshotPeriodInSlots = 30
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

	ret.directory = viper.GetString("snapshot.directory")
	if ret.directory == "" {
		ret.directory = defaultSnapshotDirectory
	}
	env.Log().Infof("%s directory is '%s'", Name, ret.directory)
	if !directoryExists(ret.directory) {
		err := os.MkdirAll(ret.directory, 0644)
		util.AssertNoError(err, "can't create snapshot directory ", ret.directory)
	}

	periodInSlots := viper.GetInt("snapshot.period_in_slots")
	if periodInSlots <= 0 {
		periodInSlots = defaultSnapshotPeriodInSlots
	}
	period := time.Duration(periodInSlots) * ledger.L().ID.SlotDuration()

	ret.keepLatest = viper.GetInt("snapshot.keep_latest")
	if ret.keepLatest <= 0 {
		ret.keepLatest = defaultKeepLatest
	}

	ret.safeSlotsBack = viper.GetInt("snapshot.safety_slots")
	if ret.safeSlotsBack == 0 {
		ret.safeSlotsBack = defaultSafetySlots
	}

	ret.registerMetrics()

	env.RepeatInBackground(Name, period, func() bool {
		ret.doSnapshot()
		ret.purgeOldSnapshots()
		return true
	}, true)

	ln := lines.New("          ").
		Add("target directory: %s", ret.directory).
		Add("frequency: %v (%d slots)", period, periodInSlots).
		Add("keep latest: %d", ret.keepLatest).
		Add("safety slot back: %d", ret.safeSlotsBack)
	ret.Log().Infof("[snapshot] work process STARTED\n%s", ln.String())
	return
}

func (s *Snapshot) registerMetrics() {
	// TODO implement snapshot metrics
}

func directoryExists(dir string) bool {
	fileInfo, err := os.Stat(dir)
	return err == nil && fileInfo.IsDir()
}

func (s *Snapshot) doSnapshot() {
	snapshotBranch := multistate.FindLatestReliableBranchAndNSlotsBack(s.StateStore(), s.safeSlotsBack, global.FractionHealthyBranch)
	if snapshotBranch == nil {
		s.Log().Errorf("[snapshot] can't find latest reliable branch")
		return
	}
	fname, stats, err := multistate.SaveSnapshot(s.StateStore(), snapshotBranch, s.Ctx(), s.directory, io.Discard)
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
