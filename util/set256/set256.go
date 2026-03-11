// Package set256 implements a set of byte values (0..255) as a 256-bit bitmap
// stored in a [32]byte array. Each bit position corresponds to a byte value.
package set256

// Set256 is a 256-bit bitmap representing a set of byte values.
// Bit i of the bitmap is set iff value i is in the set.
// Byte index = i / 8, bit within byte = i % 8 (LSB first).
type Set256 [32]byte

// Contains returns true if the set contains the given value.
func (s *Set256) Contains(v byte) bool {
	return s[v/8]&(1<<(v%8)) != 0
}

// Insert adds a value to the set.
func (s *Set256) Insert(v byte) {
	s[v/8] |= 1 << (v % 8)
}

// InsertAll adds multiple values to the set.
func (s *Set256) InsertAll(elems ...byte) {
	for _, v := range elems {
		s[v/8] |= 1 << (v % 8)
	}
}

// InsertRange adds all values from 'from' to 'toInclusive' (inclusive) to the set.
func (s *Set256) InsertRange(from, toInclusive byte) {
	for v := from; ; v++ {
		s[v/8] |= 1 << (v % 8)
		if v == toInclusive {
			break
		}
	}
}

// Remove removes a value from the set.
func (s *Set256) Remove(v byte) {
	s[v/8] &^= 1 << (v % 8)
}

// RemoveAll removes multiple values from the set.
func (s *Set256) RemoveAll(elems ...byte) {
	for _, v := range elems {
		s[v/8] &^= 1 << (v % 8)
	}
}

// IsEmpty returns true if the set contains no elements.
func (s *Set256) IsEmpty() bool {
	return *s == Set256{}
}

// Size returns the number of elements in the set (popcount).
func (s *Set256) Size() int {
	n := 0
	for _, b := range s {
		// Brian Kernighan's bit counting
		for b != 0 {
			b &= b - 1
			n++
		}
	}
	return n
}

// ForEach calls fn for each element in the set in ascending order.
// If fn returns false, iteration stops.
func (s *Set256) ForEach(fn func(byte) bool) {
	for i, b := range s {
		for b != 0 {
			bit := b & (-b)       // isolate lowest set bit
			val := byte(i*8) + bitIndex(bit)
			if !fn(val) {
				return
			}
			b &= b - 1 // clear lowest set bit
		}
	}
}

// Elements returns all elements as a sorted byte slice.
func (s *Set256) Elements() []byte {
	ret := make([]byte, 0, s.Size())
	s.ForEach(func(v byte) bool {
		ret = append(ret, v)
		return true
	})
	return ret
}

// Clone returns a copy of the set.
func (s *Set256) Clone() Set256 {
	return *s
}

// Union returns a new set containing elements from both sets.
func (s *Set256) Union(other *Set256) Set256 {
	var result Set256
	for i := range result {
		result[i] = s[i] | other[i]
	}
	return result
}

// Intersection returns a new set containing elements present in both sets.
func (s *Set256) Intersection(other *Set256) Set256 {
	var result Set256
	for i := range result {
		result[i] = s[i] & other[i]
	}
	return result
}

// Difference returns a new set containing elements in s but not in other.
func (s *Set256) Difference(other *Set256) Set256 {
	var result Set256
	for i := range result {
		result[i] = s[i] &^ other[i]
	}
	return result
}

// SymmetricDifference returns a new set containing elements in exactly one of the two sets.
func (s *Set256) SymmetricDifference(other *Set256) Set256 {
	var result Set256
	for i := range result {
		result[i] = s[i] ^ other[i]
	}
	return result
}

// IsSubsetOf returns true if every element of s is also in other.
func (s *Set256) IsSubsetOf(other *Set256) bool {
	for i := range s {
		if s[i]&^other[i] != 0 {
			return false
		}
	}
	return true
}

// Equals returns true if both sets contain exactly the same elements.
func (s *Set256) Equals(other *Set256) bool {
	return *s == *other
}

// Complement returns a new set containing all values NOT in s.
func (s *Set256) Complement() Set256 {
	var result Set256
	for i := range result {
		result[i] = ^s[i]
	}
	return result
}

// NewFromSlice creates a Set256 from a byte slice of up to 32 bytes.
// The slice is interpreted as the raw bitmap (not as element values).
// Remaining bytes are zero-padded. A nil slice produces an empty set.
func NewFromSlice(data []byte) Set256 {
	var s Set256
	copy(s[:], data)
	return s
}

// Bytes converts the Set256 into a byte slice by trimming trailing zero bytes.
// An empty set returns nil.
func (s *Set256) Bytes() []byte {
	last := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] != 0 {
			last = i
			break
		}
	}
	if last < 0 {
		return nil
	}
	ret := make([]byte, last+1)
	copy(ret, s[:last+1])
	return ret
}

// bitIndex returns the bit position (0..7) of a single-bit byte value.
func bitIndex(b byte) byte {
	// b is a power of 2 (single bit set)
	var idx byte
	if b&0xF0 != 0 {
		idx += 4
	}
	if b&0xCC != 0 {
		idx += 2
	}
	if b&0xAA != 0 {
		idx += 1
	}
	return idx
}
