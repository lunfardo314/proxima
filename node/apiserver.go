package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/lunfardo314/proxima/api"
	"github.com/lunfardo314/proxima/api/server"
	"github.com/lunfardo314/proxima/api/streaming"
	"github.com/lunfardo314/proxima/core/core_modules/tippool"
	"github.com/lunfardo314/proxima/core/workflow"
	"github.com/lunfardo314/proxima/global"
	"github.com/lunfardo314/proxima/ledger"
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/multistate"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
	"github.com/spf13/viper"
)

func (p *ProximaNode) startAPIServer() {
	if viper.GetBool("api.disable") {
		// default is enabled API
		p.Log().Infof("API server is disabled")
		return
	}
	port := viper.GetInt("api.port")
	addr := fmt.Sprintf(":%d", port)
	p.Log().Infof("starting API server on %s", addr)

	go server.Run(addr, p)
	go func() {
		<-p.Ctx().Done()
		p.stopAPIServer()
	}()

}

func (p *ProximaNode) stopAPIServer() {
	// do we need to do something else here?
	p.Log().Debugf("API server has been stopped")
}

func (p *ProximaNode) startStreaming() {
	if viper.GetBool("streaming.enable") || viper.GetBool("api.streaming_enable") {
		streaming.Run(p)
	}
}

// GetNodeInfo TODO not finished
func (p *ProximaNode) GetNodeInfo() *global.NodeInfo {
	aliveStaticPeers, aliveDynamicPeers, _ := p.peers.NumAlive()

	ret := &global.NodeInfo{
		ID:                p.peers.SelfPeerID(),
		Version:           global.Version,
		NumStaticAlive:    uint16(aliveStaticPeers),
		NumDynamicAlive:   uint16(aliveDynamicPeers),
		Sequencer:         p.GetOwnSequencerID(),
		CommitHash:        global.CommitHash,
		CommitTime:        global.CommitTime,
		MemoryStressLevel: p.MemoryStressLevel(),
		PipelineSize:      p.workflow.PipelineSize(),
		IsSyncing:         p.workflow.IsSyncing(),
		IsSnapshotting:    p.IsSnapshotting(),
	}
	return ret
}

// GetSyncInfo TODO not finished
func (p *ProximaNode) GetSyncInfo() *api.SyncInfo {
	latestSlot, latestHealthySlot, synced := p.workflow.LatestBranchSlots()
	lrb := p.GetLatestReliableBranch()
	lrbSlot := uint32(0)
	curSlot := uint32(ledger.TimeNow().Slot)
	var cov uint64
	if lrb == nil {
		p.Log().Warnf("[sync] can't find latest reliable branch")
	} else {
		cov = p.workflow.Branches().LedgerCoverage(lrb.TxID())
		lrbSlot = uint32(lrb.Stem.ID.Slot())
	}

	ret := &api.SyncInfo{
		Synced:         synced,
		CurrentSlot:    curSlot,
		LrbSlot:        lrbSlot,
		LedgerCoverage: cov,
		PerSequencer:   make(map[string]api.SequencerSyncInfo),
	}
	if p.sequencer != nil {
		ssi := api.SequencerSyncInfo{
			Synced:              synced,
			LatestHealthySlot:   uint32(latestHealthySlot),
			LatestCommittedSlot: uint32(latestSlot),
			LedgerCoverage:      p.sequencer.LedgerCoverage(),
		}
		chainId := p.sequencer.SequencerID()
		ret.PerSequencer[chainId.StringHex()] = ssi
	}
	return ret
}

func (p *ProximaNode) GetPeersInfo() *api.PeersInfo {
	return p.peers.GetPeersInfo()
}

func (p *ProximaNode) LatestReliableState() (multistate.SugaredStateReader, error) {
	lrb := p.workflow.Branches().FindLatestReliableBranch()
	if lrb == nil {
		return multistate.SugaredStateReader{}, fmt.Errorf("LatestReliableState: can't find latest reliable branch")
	}
	return multistate.MakeSugared(p.workflow.Branches().GetStateReaderForTheBranch(lrb.TxID()), p), nil
}

// DiagCompareReaders is a diagnostic helper for the 2026-04-23 consensus halt.
// It looks up the output `oid` through both state-reader paths used in production —
// GetStateReaderForTheBranch (used by the API) and GetVirtualStateReaderForTheBranch
// (used by the incremental attacher) — and reports their results side by side along
// with internal bookkeeping about the branch. Remove before shipping.
func (p *ProximaNode) DiagCompareReaders(branchID base.TransactionID, oid base.OutputID) map[string]any {
	br := p.workflow.Branches()
	snapID := br.SnapshotBranchID()
	result := map[string]any{
		"branchID":       (&branchID).StringHex(),
		"outputID":       (&oid).StringHex(),
		"isPending":      br.IsPending(branchID),
		"rootHex":        br.GetRootHex(branchID),
		"snapshotBranch": (&snapID).StringHex(),
	}

	lookupBitmap := func(rdr multistate.StateReader) any {
		if rr, ok := rdr.(*multistate.Readable); ok {
			if unspent, ok2 := rr.GetTxUnspentOutputSet(oid.TransactionID()); ok2 {
				return unspent.Elements()
			}
			return "tx record not in trie"
		}
		return "reader is not *Readable (virtual overlay)"
	}

	rdrApi := br.GetStateReaderForTheBranch(branchID)
	if rdrApi == nil {
		result["apiReader"] = "nil"
	} else {
		dataApi, foundApi := rdrApi.GetUTXO(oid)
		rApi := map[string]any{"found": foundApi, "dataLen": len(dataApi)}
		if foundApi {
			rApi["dataHex"] = hex.EncodeToString(dataApi)
		}
		rApi["bitmapElements"] = lookupBitmap(rdrApi)
		result["apiReader"] = rApi
	}

	rdrAtt := br.GetVirtualStateReaderForTheBranch(branchID)
	if rdrAtt == nil {
		result["attacherReader"] = "nil"
	} else {
		dataAtt, foundAtt := rdrAtt.GetUTXO(oid)
		rAtt := map[string]any{"found": foundAtt, "dataLen": len(dataAtt)}
		if foundAtt {
			rAtt["dataHex"] = hex.EncodeToString(dataAtt)
		}
		rAtt["bitmapElements"] = lookupBitmap(rdrAtt)
		result["attacherReader"] = rAtt
	}
	return result
}

// DiagListBranchesAtSlot diagnostic helper.
func (p *ProximaNode) DiagListBranchesAtSlot(slot uint32) []map[string]any {
	return p.workflow.Branches().DiagListBranchesAtSlot(slot)
}

// DiagAllPendingBranches diagnostic helper.
func (p *ProximaNode) DiagAllPendingBranches() []map[string]any {
	return p.workflow.Branches().DiagAllPendingBranches()
}

func (p *ProximaNode) CheckTransactionInLRB(txid base.TransactionID, maxDepth int) (lrbid base.TransactionID, foundAtDepth int) {
	return p.workflow.CheckTransactionInLRB(txid, maxDepth)
}

func (p *ProximaNode) SubmitTxBytesFromAPI(txBytes []byte) {
	p.workflow.TxBytesInFromAPIQueued(txBytes)
}

func (p *ProximaNode) GetLatestReliableBranch() (ret *multistate.BranchData) {
	err := util.CatchPanicOrError(func() error {
		ret = p.workflow.Branches().FindLatestReliableBranch()
		return nil
	})
	if err != nil {
		if errors.Is(err, common.ErrDBUnavailable) {
			return nil
		}
		p.Fatal(err)
	}
	return
}

func (p *ProximaNode) GetSnapshotBranchID() base.TransactionID {
	return multistate.FetchSnapshotBranchID(p.StateStore())
}

func (p *ProximaNode) GetSnapshotFilePath() (string, error) {
	dir := viper.GetString("snapshot.directory")
	if dir == "" {
		dir = "snapshot"
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("cannot read snapshot directory '%s': %w", dir, err)
	}

	type fileEntry struct {
		path    string
		modTime time.Time
	}
	var files []fileEntry

	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeType != 0 {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, "__tmp__") {
			continue
		}
		matched, err := filepath.Match("*.snapshot", name)
		if err != nil || !matched {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{
			path:    filepath.Join(dir, name),
			modTime: info.ModTime(),
		})
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no snapshot files found in '%s'", dir)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].modTime.After(files[j].modTime)
	})
	return files[0].path, nil
}

func (p *ProximaNode) SelfPeerID() peer.ID {
	return p.peers.SelfPeerID()
}

func (p *ProximaNode) GetKnownLatestMilestonesJSONAble() map[string]tippool.LatestSequencerTipDataJSONAble {
	return p.workflow.GetKnownLatestSequencerDataJSONAble()
}

func (p *ProximaNode) OnNewVertex(fun func(data *workflow.NewVertexEventData) bool) {
	p.workflow.OnNewVertex(fun)
}

func (p *ProximaNode) OnTxDeleted(fun func(txid base.TransactionID) bool) {
	p.workflow.OnTxDeleted(fun)
}

// TxLogOnOffAPIEnabled returns true if the txlog on/off API is enabled by node configuration.
func (p *ProximaNode) TxLogOnOffAPIEnabled() bool {
	return p.txLogOnOffAPI
}

// TxLogEnable enables or disables the transaction logger with the specified level.
func (p *ProximaNode) TxLogEnable(level global.TxLogLevel) {
	if p.txLogger != nil {
		p.txLogger.TxLogEnable(level)
	}
}

// TxLogGet retrieves log records by transaction ID prefix.
// Returns records sorted by timestamp in ascending order.
func (p *ProximaNode) TxLogGet(txShortIDPrefix []byte, max ...int) ([]global.TxLogRecord, error) {
	if p.txLogger == nil {
		return nil, fmt.Errorf("transaction logger not initialized")
	}
	return p.txLogger.TxLogGet(txShortIDPrefix, max...)
}

// TxLogIterate iterates over log records starting from the given time.
func (p *ProximaNode) TxLogIterate(begin time.Time, fun func(rec global.TxLogRecord)) error {
	if p.txLogger == nil {
		return fmt.Errorf("transaction logger not initialized")
	}
	return p.txLogger.TxLogIterate(begin, fun)
}

// TxLogIsEnabled returns true if the transaction logger is enabled.
func (p *ProximaNode) TxLogIsEnabled() bool {
	return p.txLogger != nil && p.txLogger.IsEnabled()
}

// TxLogLevel returns the current transaction log level.
func (p *ProximaNode) TxLogLevel() global.TxLogLevel {
	if p.txLogger == nil {
		return global.TxLogLevelOff
	}
	return p.txLogger.Level()
}
