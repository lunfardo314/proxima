package bitset

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

type Bitset [32]byte

func New(elems ...byte) (ret Bitset) {
	ret.Insert(elems...)
	return
}

func NewWithFirstElements(n byte) (ret Bitset) {
	for i := 0; i < int(n); i++ {
		ret.insert(byte(i))
	}
	return
}

func FromBytes(data []byte) (ret Bitset, err error) {
	if len(data) > 32 {
		err = errors.New("FromBytes: wrong data length")
		return
	}
	copy(ret[:], data)
	return
}

// Bytes returns at least 1 byte, max 32 bytes
func (b *Bitset) Bytes() []byte {
	// largest non=-zero byte
	idx := -1
	for i := 31; i >= 0; i-- {
		if b[i] != 0 {
			idx = i
			break
		}
	}
	if idx < 0 {
		// empty
		return []byte{0}
	}
	ret := make([]byte, idx+1)
	copy(ret, b[:])
	return ret
}

func (b *Bitset) Contains(idx byte) bool {
	pos := idx / 8
	bit := idx % 8

	return b[pos]&(byte(0x1)<<bit) != 0
}

func (b *Bitset) Insert(idx ...byte) {
	for _, i := range idx {
		b.insert(i)
	}
}

func (b *Bitset) insert(idx byte) {
	pos := idx / 8
	bit := idx % 8

	b[pos] |= 0x1 << bit
}

func (b *Bitset) Remove(idx byte) {
	pos := idx / 8
	bit := idx % 8

	b[pos] &= ^(0x1 << bit)
}

func (b *Bitset) String() string {
	ln := make([]string, 0)
	for i := 0; i < 256; i++ {
		if b.Contains(byte(i)) {
			ln = append(ln, strconv.Itoa(i))
		}
	}
	return fmt.Sprintf("{%s}", strings.Join(ln, ","))
}
