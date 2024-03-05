package ledger

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"

	"github.com/lunfardo314/proxima/util"
)

const HistoryCoverageDeltas = 2

func init() {
	util.Assertf(1 < HistoryCoverageDeltas && HistoryCoverageDeltas*8 <= 256, "HistoryCoverageDeltas*8 <= 256")
}

type Coverage [HistoryCoverageDeltas]uint64

func (lc *Coverage) Shift(shift int) Coverage {
	ret := Coverage{}
	if shift < HistoryCoverageDeltas {
		copy(ret[shift:], lc[:])
	}
	return ret
}

func (lc *Coverage) AddDelta(a uint64) {
	util.Assertf(lc[0] <= math.MaxUint64-a, "Coverage.AddDelta: overflow")
	lc[0] += a
}

func (lc *Coverage) LatestDelta() uint64 {
	return lc[0]
}

func (lc *Coverage) Sum() (ret uint64) {
	if lc == nil {
		return 0
	}
	for _, v := range lc {
		ret += v
	}
	return
}

func (lc *Coverage) Bytes() []byte {
	util.Assertf(len(lc) == HistoryCoverageDeltas, "len(lc) == HistoryCoverageDeltas")
	ret := make([]byte, len(lc)*8)
	for i, d := range lc {
		binary.BigEndian.PutUint64(ret[i*8:(i+1)*8], d)
	}
	return ret
}

//
//func (lc *Coverage) BytesOfBranchCoverage() []byte {
//	util.Assertf(len(lc) == HistoryCoverageDeltas, "len(lc) == HistoryCoverageDeltas")
//	ret := make([]byte, (HistoryCoverageDeltas-1)*8)
//	for i := range lc {
//		if i == 0 {
//			continue
//		}
//		binary.BigEndian.PutUint64(ret[(i-1)*8:i*8], lc[i])
//	}
//	return ret
//}

func (lc *Coverage) String() string {
	if lc == nil {
		return "0"
	}
	all := make([]string, len(lc))
	for i, c := range lc {
		all[i] = util.GoTh(c)
	}
	return fmt.Sprintf("sum(%s)->%s", strings.Join(all, ", "), util.GoTh(lc.Sum()))
}

func (lc *Coverage) StringShort() string {
	if lc == nil {
		return "0"
	}
	all := make([]string, len(lc))
	for i, c := range lc {
		all[i] = util.GoTh(c)
	}
	return fmt.Sprintf("(%s)", strings.Join(all, ", "))
}

func CoverageFromBytes(data []byte) (ret Coverage, err error) {
	if len(data) != HistoryCoverageDeltas*8 {
		err = fmt.Errorf("CoverageFromBytes: wrong data size")
		return
	}
	for i := 0; i < HistoryCoverageDeltas; i++ {
		ret[i] = binary.BigEndian.Uint64(data[i*8 : (i+1)*8])
	}
	return
}

func CoverageFromArray(arr []uint64) (ret Coverage, err error) {
	if len(arr) != HistoryCoverageDeltas {
		err = fmt.Errorf("CoverageFromArray: wrong number of array elements")
		return
	}
	copy(ret[:], arr)
	return
}
