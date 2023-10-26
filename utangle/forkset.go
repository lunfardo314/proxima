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

func (fs ForkSet) Clone() ForkSet {
	return util.CloneMapShallow(fs)
}

func (fs ForkSet) Lines(prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	sorted := util.KeysSorted(fs, func(o1, o2 WrappedOutput) bool {
		return o1.Less(&o2)
	})
	for _, o := range sorted {
		ret.Add(NewFork(o, fs[o]).String())
	}
	return ret
}

func (fs ForkSet) ConflictsWith(f Fork) bool {
	sn, found := fs[f.ConflictSetID]
	return found && sn != f.SN
}

func (fs ForkSet) Insert(f Fork) bool {
	sn, found := fs[f.ConflictSetID]
	if found {
		return f.SN == sn
	}
	fs[f.ConflictSetID] = f.SN
	return true
}

func HasConflict(fs1, fs2 ForkSet) (conflict WrappedOutput) {
	for csid, sn := range fs1 {
		if fs2.ConflictsWith(NewFork(csid, sn)) {
			conflict = csid
			return
		}
	}
	return
}

// Absorb in case of conflict receiver is not consistent
func (fs ForkSet) Absorb(fs1 ForkSet) WrappedOutput {
	for csid, sn := range fs1 {
		if !fs.Insert(NewFork(csid, sn)) {
			return csid
		}
	}
	return WrappedOutput{}
}

// AbsorbSafe same as Absorb but leaves receiver untouched in case of conflict
func (fs ForkSet) AbsorbSafe(fs1 ForkSet) (conflict WrappedOutput) {
	if conflict = HasConflict(fs, fs1); conflict.VID != nil {
		return
	}
	for csid, sn := range fs1 {
		fs[csid] = sn
	}
	return
}

func (fs ForkSet) AbsorbVIDSafe(vid *WrappedTx) (conflict WrappedOutput) {
	vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
		conflict = fs.AbsorbSafe(v.forks)
	}})
	return
}

func (fs ForkSet) ContainsOutput(wOut WrappedOutput) (ret bool) {
	_, ret = fs[wOut]
	return
}

// MergeForkSets fork sets are non-deterministic, therefore result of merging in npt deterministic too
// It is guaranteed that conflict sets made up of sequencer outputs are all propagated.
// So, if a set of sequencer transactions produce conflict-less merge, the resulting fork set will include
// each fork set from the past cone
func MergeForkSets(vids ...*WrappedTx) (ret ForkSet, conflict WrappedOutput) {
	if len(vids) == 0 {
		ret = make(ForkSet)
		return
	}
	for i, vid := range vids {
		vid.Unwrap(UnwrapOptions{Vertex: func(v *Vertex) {
			if i == 0 {
				ret = v.forks.Clone()
			} else {
				conflict = ret.Absorb(v.forks)
			}
		}})
		if conflict.VID != nil {
			ret = nil
			return
		}
	}
	return
}

//
//// BaselineBranch baseline branch is the latest branch in the fork set, if any
////
//func (fs ForkSet) BaselineBranch() (ret *WrappedTx) {
//	first := true
//	for conflictSetID := range fs {
//		if !conflictSetID.VID.IsBranchTransaction() {
//			continue
//		}
//		if first {
//			ret = conflictSetID.VID
//			first = false
//			continue
//		}
//		if conflictSetID.Timestamp().After(ret.Timestamp()) {
//			ret = conflictSetID.VID
//		}
//	}
//	return
//}
