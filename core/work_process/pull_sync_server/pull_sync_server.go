package pull_sync_server

import (
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/core/syncmgr"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/multistate"
	"github.com/lunfardo314/proxima/peering"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/queue"
)

// This consumer responds to 'pull sync portion' requests from other nodes by sending back branch transactions
// which are in certain amount of consecutive slots after given slot.
// Only branches which are included into at least one current tip are sent.
// In most cases it will be sending a chain of branches starting from the given one: one branch per slot
// Number of branches sent back is capped by constant and by the request data.
// This function is used for the linear, gradual syncing, when node is synced portion by portion.
// It allows us to avoid potentially unbounded number of attacher goroutines which may be started
// when syncing too many slots back

type (
	Environment interface {
		global.NodeGlobal
		StateStore() global.StateStore
		SendTx(sendTo peer.ID, txids ...ledger.TransactionID)
		IsSyncedWithNetwork() bool
		TipBranchHasTransaction(branchID, txid *ledger.TransactionID) bool
	}

	Input struct {
		StartFrom ledger.Slot
		MaxSlots  int
		PeerID    peer.ID
	}

	PullSyncServer struct {
		*queue.Queue[*Input]
		Environment
	}
)

const (
	Name           = "pullSyncServer"
	TraceTag       = Name
	chanBufferSize = 10
)

func New(env Environment) *PullSyncServer {
	return &PullSyncServer{
		Queue:       queue.NewQueueWithBufferSize[*Input](Name, chanBufferSize, env.Log().Level(), nil),
		Environment: env,
	}
}

func (d *PullSyncServer) Start() {
	d.MarkWorkProcessStarted(Name)
	d.AddOnClosed(func() {
		d.MarkWorkProcessStopped(Name)
	})
	d.Queue.Start(d, d.Ctx())
}

func (d *PullSyncServer) Consume(inp *Input) {
	if !d.IsSyncedWithNetwork() && !d.IsBootstrapNode() {
		d.Environment.Log().Warnf("[PullSyncServer]: can't respond to sync request: node itself is out of sync and is not a bootstrap node")
		return
	}
	maxSlots := inp.MaxSlots
	if maxSlots > global.MaxSyncPortionSlots {
		maxSlots = global.MaxSyncPortionSlots
	}
	d.Environment.Log().Infof("[PullSyncServer] pull sync portion request for slots from slot %d, up to %d slots ",
		inp.StartFrom, maxSlots)

	latestHealthySlot := multistate.FindLatestHealthySlot(d.StateStore(), syncmgr.FractionHealthyBranchCriterion)
	slotNow := ledger.TimeNow().Slot()

	startFromSlot := inp.StartFrom
	if latestHealthySlot <= startFromSlot {
		// it seems this node knows less than the requester
		if latestHealthySlot < 5 {
			startFromSlot = 0
		} else {
			startFromSlot = latestHealthySlot - 5
		}
		d.Environment.Log().Infof("[PullSyncServer] pull sync portion adjusted to start from slot %d. latestHealthySlot: %d",
			startFromSlot, latestHealthySlot)
	}

	branchIDs := make([]ledger.TransactionID, 0)

	// collect tips
	tipRoots := multistate.FetchRootRecordsNSlotsBack(d.StateStore(), 1)
	util.Assertf(len(tipRoots) > 0, "len(tipRoots)>0")
	tipBranches := multistate.FetchBranchDataMulti(d.StateStore(), tipRoots...)
	tipIDs := make([]ledger.TransactionID, len(tipBranches))
	for i, tipBranchData := range tipBranches {
		if tipBranchData.IsHealthy(syncmgr.FractionHealthyBranchCriterion) {
			tipIDs[i] = tipBranchData.Stem.ID.TransactionID()
		}
	}

	nslots := 0
	var lastSlot ledger.Slot
	store := d.StateStore()

	util.Assertf(startFromSlot < latestHealthySlot, "startFromSlot < latestHealthySlot")

	for slot := startFromSlot; nslots < maxSlots && slot < latestHealthySlot && slot < slotNow; slot++ {
		// fetch branches of the slot
		branches := multistate.FetchBranchDataMulti(store, multistate.FetchRootRecords(store, slot)...)
		// collect branches from the slot which are included into any of the tips
		// In most cases it will be exactly one branch. It will be zero if slot is skipped
		slotContainsBranch := false
		for _, branchData := range branches {
			txid := branchData.Stem.ID.TransactionID()
			// check every tip if it has transaction in the state
			for i := range tipIDs {
				if d.TipBranchHasTransaction(&tipIDs[i], &txid) {
					branchIDs = append(branchIDs, txid)
					if !slotContainsBranch {
						nslots++
						lastSlot = slot
						slotContainsBranch = true
					}
					break
				}
			}
		}
	}
	if len(branchIDs) > 0 {
		// branches already sorted ascending by slot number
		d.SendTx(inp.PeerID, branchIDs...)

		d.Environment.Log().Infof("[PullSyncServer]: sync portion of %d branches -> %s. Slots from %d to %d",
			len(branchIDs), peering.ShortPeerIDString(inp.PeerID), startFromSlot, lastSlot)
	} else {
		d.Environment.Log().Warnf("[PullSyncServer]: empty sync portion from slot %d", inp.StartFrom)
	}
}
