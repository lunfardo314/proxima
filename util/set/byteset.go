package set

import "fmt"

type ByteSet [32]byte

func NewBytesSet(el ...byte) *ByteSet {
	return new(ByteSet).Add(el...)
}

func _idxMask(el byte) (byte, byte) {
	return el / 8, byte(0x01) << (el % 8)
}

func (s *ByteSet) Contains(el byte) bool {
	idx, mask := _idxMask(el)
	return s[idx]&mask != 0
}

func (s *ByteSet) Add(elem ...byte) *ByteSet {
	for _, el := range elem {
		idx, mask := _idxMask(el)
		s[idx] |= mask
	}
	return s
}

func (s *ByteSet) Remove(elem ...byte) *ByteSet {
	for _, el := range elem {
		idx, mask := _idxMask(el)
		s[idx] &= ^mask
	}
	return s
}

func UnionByteSet(s1, s2 ByteSet) (ret ByteSet) {
	for i := range ret {
		ret[i] = s1[i] | s2[i]
	}
	return
}

func (s *ByteSet) Clone() *ByteSet {
	return &(*s)
}

func (s *ByteSet) Size() (ret int) {
	for i := 0; i < 256; i++ {
		if s.Contains(byte(i)) {
			ret++
		}
	}
	return
}

func (s *ByteSet) IsEmpty() bool {
	return *s == ByteSet{}
}

func (s *ByteSet) ForEach(fun func(el byte) bool) {
	for i := 0; i < 256; i++ {
		if s.Contains(byte(i)) {
			if !fun(byte(i)) {
				return
			}
		}
	}
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
