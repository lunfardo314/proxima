package utangle

import "github.com/lunfardo314/proxima/util"

type (
	Fork struct {
		ConflictSetID WrappedOutput
		SN            uint16
	}

	ForkSet map[WrappedOutput]uint16
)

func NewFork(wOut WrappedOutput, forkSN uint16) Fork {
	return Fork{
		ConflictSetID: wOut,
		SN:            forkSN,
	}
}

func (f Fork) ConflictsWith(fs ForkSet) bool {
	return fs.ConflictsWith(f)
}

func (fs ForkSet) Clone() ForkSet {
	return util.CloneMapShallow(fs)
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

func (fs ForkSet) Absorb(fs1 ForkSet) WrappedOutput {
	for csid, sn := range fs1 {
		if !fs.Insert(NewFork(csid, sn)) {
			return csid
		}
	}
	return WrappedOutput{}
}

// AbsorbAtomic same as Absorb but leaves receiver untouched in case of conflict
func (fs ForkSet) AbsorbAtomic(fs1 ForkSet) (conflict WrappedOutput) {
	if conflict = HasConflict(fs, fs1); conflict.VID != nil {
		return
	}
	for csid, sn := range fs1 {
		fs[csid] = sn
	}
	return
}

func Merge(fSets ...ForkSet) (ret ForkSet, conflict WrappedOutput) {
	if len(fSets) == 0 {
		ret = make(ForkSet)
		return
	}
	for i, fs := range fSets {
		if i == 0 {
			ret = fSets[0].Clone()
			continue
		}
		conflict = ret.Absorb(fs)
		if conflict.VID != nil {
			return nil, conflict
		}
	}
	return
}

// BaselineBranch baseline branch is the latest branch in the fork set, if any
func (fs ForkSet) BaselineBranch() (ret *WrappedTx) {
	first := true
	for conflictSetID := range fs {
		if !conflictSetID.VID.IsBranchTransaction() {
			continue
		}
		if first {
			ret = conflictSetID.VID
			first = false
			continue
		}
		if conflictSetID.Timestamp().After(ret.Timestamp()) {
			ret = conflictSetID.VID
		}
	}
	return
}
