package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

// ForkSNReserved is fork serial number for temporary attachment
// Attached vertices will have serial numbers less than the reserved one
const ForkSNReserved = byte(0xff)

func newFork(wOut WrappedOutput, forkSN byte) Fork {
	return Fork{
		ConflictSetID: wOut,
		SN:            forkSN,
	}
}

func (f Fork) String() string {
	return fmt.Sprintf("%s:%d", f.ConflictSetID.IDShort(), f.SN)
}

func newForkSet() *forkSet {
	return &forkSet{
		m: make(map[WrappedOutput]byte),
	}
}

func (fs *forkSet) lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	sorted := util.KeysSorted(fs.m, func(o1, o2 WrappedOutput) bool {
		return o1.Less(&o2)
	})
	for _, o := range sorted {
		ret.Add(newFork(o, fs.m[o]).String())
	}
	return ret
}

func (fs *forkSet) conflictsWith(f Fork) bool {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	sn, found := fs.m[f.ConflictSetID]
	return found && sn != f.SN
}

func (fs *forkSet) insert(f Fork) bool {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	sn, found := fs.m[f.ConflictSetID]
	if found {
		return f.SN == sn
	}
	fs.m[f.ConflictSetID] = f.SN
	return true
}

func hasConflict(fs1, fs2 *forkSet) (conflict WrappedOutput) {
	if fs1 == fs2 {
		return
	}

	fs1.mutex.RLock()
	defer fs1.mutex.RUnlock()

	for csid, sn := range fs1.m {
		if fs2.conflictsWith(newFork(csid, sn)) {
			conflict = csid
			return
		}
	}
	return
}

// absorb in case of conflict receiver is not consistent
func (fs *forkSet) absorb(fs1 *forkSet) (ret WrappedOutput) {
	if fs == fs1 || fs1 == nil {
		return
	}

	fs1.mutex.RLock()
	defer fs1.mutex.RUnlock()

	for csid, sn := range fs1.m {
		if !fs.insert(newFork(csid, sn)) {
			return csid
		}
	}
	return
}

// absorbSafe same as absorb but leaves receiver untouched in case of conflict
func (fs *forkSet) absorbSafe(fs1 *forkSet) (conflict WrappedOutput) {
	if fs == fs1 || fs1 == nil {
		return
	}

	if conflict = hasConflict(fs, fs1); conflict.VID != nil {
		return
	}

	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	fs1.mutex.RLock()
	defer fs1.mutex.RUnlock()

	for csid, sn := range fs1.m {
		fs.m[csid] = sn
	}
	return
}

func (fs *forkSet) cleanOrphaned() {
	if fs == nil {
		return
	}
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	toDeleteForks := make([]WrappedOutput, 0)
	for wOut := range fs.m {
		if wOut.VID.IsOrphaned() {
			toDeleteForks = append(toDeleteForks, wOut)
		}
	}
	for _, wOut := range toDeleteForks {
		delete(fs.m, wOut)
	}
}
