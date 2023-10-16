package set

import (
	"encoding/binary"
	"fmt"

	"github.com/lunfardo314/proxima/util/lines"
)

type ByteSet [4]uint64

func NewByteSet(el ...byte) (ret ByteSet) {
	ret.Insert(el...)
	return
}

const (
	div64mask = ^byte(0x3f)
	rem64mask = ^div64mask
)

func _idxMask(el byte) (int, uint64) {
	nIdx := int((el & div64mask) >> 6)
	mask := uint64(0x1) << (el & rem64mask)
	return nIdx, mask
}

func (s *ByteSet) stringMask() string {
	ln := lines.New()
	for i, u := range s {
		ln.Add("%d : %064b", i, u)
	}
	return ln.String()
}

func (s *ByteSet) Contains(el byte) bool {
	idx, mask := _idxMask(el)
	return s[idx]&mask != 0
}

func (s *ByteSet) Insert(elem ...byte) {
	for _, el := range elem {
		idx, mask := _idxMask(el)
		s[idx] |= mask
	}
}

func (s *ByteSet) Remove(elem ...byte) {
	for _, el := range elem {
		idx, mask := _idxMask(el)
		s[idx] &= ^mask
	}
}

func UnionByteSet(s1, s2 ByteSet) (ret ByteSet) {
	for i := range ret {
		ret[i] = s1[i] | s2[i]
	}
	return
}

func (s *ByteSet) Clone() (ret ByteSet) {
	ret = *s
	return
}

func (s *ByteSet) Size() (ret int) {
	for i := 0; i < 256; i++ {
		if s.Contains(byte(i)) {
			ret++
		}
	}
	return
}

func _forEachOneInUint64(n uint64, fun func(i byte) bool) bool {
	var bin [8]byte
	binary.LittleEndian.PutUint64(bin[:], n)
	for i, b := range bin {
		if b == 0 {
			continue
		}
		for j := 0; j < 8; j++ {
			if b&(byte(0x1)<<j) != 0 {
				if !fun(byte(i*8 + j)) {
					return false
				}
			}
		}
	}
	return true
}

func (s *ByteSet) ForEach(fun func(el byte) bool) {
	for i, u := range s {
		if u == 0 {
			continue
		}
		ret := _forEachOneInUint64(u, func(i1 byte) bool {
			return fun(byte(i*64) + i1)
		})
		if !ret {
			return
		}
	}
}

var EmptyByteSet = ByteSet{}

func (s *ByteSet) IsEmpty() bool {
	return *s == EmptyByteSet
}

func (s *ByteSet) AsList() []byte {
	ret := make([]byte, 0)
	s.ForEach(func(el byte) bool {
		ret = append(ret, el)
		return true
	})
	return ret
}

func (s *ByteSet) String() string {
	return fmt.Sprintf("%+v", s.AsList())
}
