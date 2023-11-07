package utangle

import (
	"fmt"

	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

func NewFork(wOut WrappedOutput, forkSN byte) Fork {
	return Fork{
		ConflictSetID: wOut,
		SN:            forkSN,
	}
}

func (f Fork) String() string {
	return fmt.Sprintf("%s:%d", f.ConflictSetID.IDShort(), f.SN)
}

func NewForkSet() *ForkSet {
	return &ForkSet{
		m: make(map[WrappedOutput]byte),
	}
}

func (fs *ForkSet) Clone() *ForkSet {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	return &ForkSet{m: util.CloneMapShallow(fs.m)}
}

func (fs *ForkSet) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)

	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	sorted := util.KeysSorted(fs.m, func(o1, o2 WrappedOutput) bool {
		return o1.Less(&o2)
	})
	for _, o := range sorted {
		ret.Add(NewFork(o, fs.m[o]).String())
	}
	return ret
}

func (fs *ForkSet) ConflictsWith(f Fork) bool {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	sn, found := fs.m[f.ConflictSetID]
	return found && sn != f.SN
}

func (fs *ForkSet) Insert(f Fork) bool {
	fs.mutex.Lock()
	defer fs.mutex.Unlock()

	sn, found := fs.m[f.ConflictSetID]
	if found {
		return f.SN == sn
	}
	fs.m[f.ConflictSetID] = f.SN
	return true
}

func HasConflict(fs1, fs2 *ForkSet) (conflict WrappedOutput) {
	if fs1 == fs2 {
		return
	}

	fs1.mutex.RLock()
	defer fs1.mutex.RUnlock()

	for csid, sn := range fs1.m {
		if fs2.ConflictsWith(NewFork(csid, sn)) {
			conflict = csid
			return
		}
	}
	return
}

// Absorb in case of conflict receiver is not consistent
func (fs *ForkSet) Absorb(fs1 *ForkSet) (ret WrappedOutput) {
	if fs == fs1 || fs1 == nil {
		return
	}

	fs1.mutex.RLock()
	defer fs1.mutex.RUnlock()

	for csid, sn := range fs1.m {
		if !fs.Insert(NewFork(csid, sn)) {
			return csid
		}
	}
	return
}

// AbsorbSafe same as Absorb but leaves receiver untouched in case of conflict
func (fs *ForkSet) AbsorbSafe(fs1 *ForkSet) (conflict WrappedOutput) {
	if fs == fs1 || fs1 == nil {
		return
	}

	if conflict = HasConflict(fs, fs1); conflict.VID != nil {
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

func (fs *ForkSet) ContainsOutput(wOut WrappedOutput) (ret bool) {
	fs.mutex.RLock()
	defer fs.mutex.RUnlock()

	_, ret = fs.m[wOut]
	return
}

func (fs *ForkSet) CleanOrphaned() {
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
