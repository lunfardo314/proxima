package multistate

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/lunfardo314/proxima/util"
)

func (lc *LedgerCoverage) MakeNext(shift int) LedgerCoverage {
	ret := LedgerCoverage{}
	if shift < HistoryCoverageDeltas {
		copy(ret[shift:], lc[:])
	}
	return ret
}

func (lc *LedgerCoverage) AddDelta(a uint64) {
	util.Assertf(lc[0] <= math.MaxUint64-a, "LedgerCoverage.AddDelta: overflow")
	lc[0] += a
}

func (lc *LedgerCoverage) LatestDelta() uint64 {
	return lc[0]
}

func (lc *LedgerCoverage) Sum() (ret uint64) {
	if lc == nil {
		return 0
	}
	for _, v := range lc {
		ret += v
	}
	return
}

func (lc *LedgerCoverage) BytesOld() []byte {
	util.Assertf(len(lc) == HistoryCoverageDeltas, "len(lc) == HistoryCoverageDeltas")
	ret := make([]byte, len(lc)*8)
	for i, d := range lc {
		binary.BigEndian.PutUint64(ret[i*8:(i+1)*8], d)
	}
	return ret
}

func (lc *LedgerCoverage) BytesOfBranchCoverage() []byte {
	util.Assertf(len(lc) == HistoryCoverageDeltas, "len(lc) == HistoryCoverageDeltas")
	util.Assertf(lc[0] == 0, "coverage of the branch must have latest delta == 0")
	ret := make([]byte, (HistoryCoverageDeltas-1)*8)
	for i := range lc {
		if i == 0 {
			continue
		}
		binary.BigEndian.PutUint64(ret[(i-1)*8:i*8], lc[i])
	}
	return ret
}

func (lc *LedgerCoverage) String() string {
	if lc == nil {
		return "0"
	}
	all := make([]string, len(lc))
	for i, c := range lc {
		all[i] = util.GoThousands(c)
	}
	return fmt.Sprintf("sum(%s)->%s", strings.Join(all, ", "), util.GoThousands(lc.Sum()))
}

func (lc *LedgerCoverage) StringShort() string {
	if lc == nil {
		return "0"
	}
	all := make([]string, len(lc))
	for i, c := range lc {
		all[i] = util.GoThousands(c)
	}
	return fmt.Sprintf("(%s)", strings.Join(all, ", "))
}

func BranchLedgerCoverageFromBytes(data []byte) (ret LedgerCoverage, err error) {
	if len(data) != (HistoryCoverageDeltas-1)*8 {
		err = fmt.Errorf("LedgerCoverageFromBytes: wrong data size")
		return
	}
	for i := 0; i < HistoryCoverageDeltas-1; i++ {
		ret[i+1] = binary.BigEndian.Uint64(data[i*8 : (i+1)*8])
	}
	return
}
