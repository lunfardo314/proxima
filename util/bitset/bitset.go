package bitset

import "errors"

type Bitset [32]byte

func BitsetFromBytes(data []byte) (ret Bitset, err error) {
	if len(data) > 32 {
		err = errors.New("BitsetFromBytes: wrong data length")
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

func (b *Bitset) Insert(idx byte) {
	pos := idx / 8
	bit := idx % 8

	b[pos] |= 0x1 << bit
}

func (b *Bitset) Remove(idx byte) {
	pos := idx / 8
	bit := idx % 8

	b[pos] &= ^(0x1 << bit)
}
