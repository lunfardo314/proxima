package set256

import (
	"testing"
)

// TestInsertContains verifies basic insert and membership checks.
func TestInsertContains(t *testing.T) {
	var s Set256
	if s.Contains(0) {
		t.Fatal("empty set should not contain 0")
	}
	s.Insert(0)
	if !s.Contains(0) {
		t.Fatal("set should contain 0 after insert")
	}
	// insert several values across byte boundaries
	for _, v := range []byte{1, 7, 8, 127, 128, 255} {
		s.Insert(v)
	}
	for _, v := range []byte{0, 1, 7, 8, 127, 128, 255} {
		if !s.Contains(v) {
			t.Fatalf("set should contain %d", v)
		}
	}
	// values not inserted should be absent
	for _, v := range []byte{2, 9, 126, 129, 254} {
		if s.Contains(v) {
			t.Fatalf("set should not contain %d", v)
		}
	}
}

// TestRemove verifies element removal.
func TestRemove(t *testing.T) {
	var s Set256
	s.Insert(42)
	s.Insert(43)
	s.Remove(42)
	if s.Contains(42) {
		t.Fatal("42 should be removed")
	}
	if !s.Contains(43) {
		t.Fatal("43 should still be present")
	}
	// removing a non-existent element is a no-op
	s.Remove(100)
	if s.Contains(100) {
		t.Fatal("100 was never inserted")
	}
}

// TestInsertAll verifies batch insertion.
func TestInsertAll(t *testing.T) {
	var s Set256
	s.InsertAll(0, 5, 10, 255)
	if s.Size() != 4 {
		t.Fatalf("expected 4 elements, got %d", s.Size())
	}
	for _, v := range []byte{0, 5, 10, 255} {
		if !s.Contains(v) {
			t.Fatalf("should contain %d", v)
		}
	}
	// calling with no args is a no-op
	s.InsertAll()
	if s.Size() != 4 {
		t.Fatal("InsertAll() with no args should be a no-op")
	}
}

// TestInsertRange verifies range insertion.
func TestInsertRange(t *testing.T) {
	var s Set256
	s.InsertRange(10, 15)
	if s.Size() != 6 {
		t.Fatalf("expected 6 elements, got %d", s.Size())
	}
	for v := byte(10); v <= 15; v++ {
		if !s.Contains(v) {
			t.Fatalf("should contain %d", v)
		}
	}
	if s.Contains(9) || s.Contains(16) {
		t.Fatal("should not contain values outside range")
	}
}

// TestInsertRangeSingleElement verifies range with from == toInclusive.
func TestInsertRangeSingleElement(t *testing.T) {
	var s Set256
	s.InsertRange(42, 42)
	if s.Size() != 1 || !s.Contains(42) {
		t.Fatal("single-element range should insert exactly one element")
	}
}

// TestInsertRangeFull verifies range spanning the entire 0..255 space.
func TestInsertRangeFull(t *testing.T) {
	var s Set256
	s.InsertRange(0, 255)
	if s.Size() != 256 {
		t.Fatalf("full range should have 256 elements, got %d", s.Size())
	}
}

// TestInsertRangeByteBoundary verifies range crossing a byte boundary.
func TestInsertRangeByteBoundary(t *testing.T) {
	var s Set256
	s.InsertRange(6, 10) // crosses byte boundary at 8
	expected := []byte{6, 7, 8, 9, 10}
	if s.Size() != len(expected) {
		t.Fatalf("expected %d elements, got %d", len(expected), s.Size())
	}
	for _, v := range expected {
		if !s.Contains(v) {
			t.Fatalf("should contain %d", v)
		}
	}
}

// TestRemoveAll verifies batch removal.
func TestRemoveAll(t *testing.T) {
	var s Set256
	s.InsertRange(0, 10)
	s.RemoveAll(0, 5, 10)
	if s.Size() != 8 {
		t.Fatalf("expected 8 elements, got %d", s.Size())
	}
	for _, v := range []byte{0, 5, 10} {
		if s.Contains(v) {
			t.Fatalf("should not contain %d after removal", v)
		}
	}
	// removing non-existent elements is a no-op
	s.RemoveAll(200, 201)
	if s.Size() != 8 {
		t.Fatal("removing non-existent elements should not change size")
	}
	// calling with no args is a no-op
	s.RemoveAll()
	if s.Size() != 8 {
		t.Fatal("RemoveAll() with no args should be a no-op")
	}
}

// TestIsEmpty checks the empty predicate.
func TestIsEmpty(t *testing.T) {
	var s Set256
	if !s.IsEmpty() {
		t.Fatal("zero-value should be empty")
	}
	s.Insert(0)
	if s.IsEmpty() {
		t.Fatal("set with element 0 should not be empty")
	}
	s.Remove(0)
	if !s.IsEmpty() {
		t.Fatal("set should be empty after removing the only element")
	}
}

// TestSize verifies population count.
func TestSize(t *testing.T) {
	var s Set256
	if s.Size() != 0 {
		t.Fatal("empty set size should be 0")
	}
	// insert all 256 values
	for i := 0; i < 256; i++ {
		s.Insert(byte(i))
	}
	if s.Size() != 256 {
		t.Fatalf("full set size should be 256, got %d", s.Size())
	}
	s.Remove(0)
	s.Remove(255)
	if s.Size() != 254 {
		t.Fatalf("expected 254, got %d", s.Size())
	}
}

// TestForEach checks that iteration produces sorted ascending order.
func TestForEach(t *testing.T) {
	var s Set256
	vals := []byte{200, 3, 0, 128, 50}
	for _, v := range vals {
		s.Insert(v)
	}
	var got []byte
	s.ForEach(func(v byte) bool {
		got = append(got, v)
		return true
	})
	expected := []byte{0, 3, 50, 128, 200}
	if len(got) != len(expected) {
		t.Fatalf("expected %d elements, got %d", len(expected), len(got))
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("element %d: expected %d, got %d", i, expected[i], got[i])
		}
	}
}

// TestForEachEarlyStop verifies that returning false stops iteration.
func TestForEachEarlyStop(t *testing.T) {
	var s Set256
	s.Insert(1)
	s.Insert(2)
	s.Insert(3)
	count := 0
	s.ForEach(func(v byte) bool {
		count++
		return v != 2 // stop after seeing 2
	})
	if count != 2 {
		t.Fatalf("expected 2 iterations, got %d", count)
	}
}

// TestElements verifies the Elements() helper.
func TestElements(t *testing.T) {
	var s Set256
	if len(s.Elements()) != 0 {
		t.Fatal("empty set should return empty slice")
	}
	s.Insert(255)
	s.Insert(0)
	elems := s.Elements()
	if len(elems) != 2 || elems[0] != 0 || elems[1] != 255 {
		t.Fatalf("unexpected elements: %v", elems)
	}
}

// TestClone checks that Clone produces an independent copy.
func TestClone(t *testing.T) {
	var s Set256
	s.Insert(10)
	c := s.Clone()
	c.Insert(20)
	if s.Contains(20) {
		t.Fatal("modifying clone should not affect original")
	}
	if !c.Contains(10) || !c.Contains(20) {
		t.Fatal("clone should have both elements")
	}
}

// TestUnion verifies set union.
func TestUnion(t *testing.T) {
	var a, b Set256
	a.Insert(1)
	a.Insert(2)
	b.Insert(2)
	b.Insert(3)
	u := a.Union(&b)
	for _, v := range []byte{1, 2, 3} {
		if !u.Contains(v) {
			t.Fatalf("union should contain %d", v)
		}
	}
	if u.Size() != 3 {
		t.Fatalf("union size should be 3, got %d", u.Size())
	}
}

// TestIntersection verifies set intersection.
func TestIntersection(t *testing.T) {
	var a, b Set256
	a.Insert(1)
	a.Insert(2)
	a.Insert(3)
	b.Insert(2)
	b.Insert(3)
	b.Insert(4)
	inter := a.Intersection(&b)
	if inter.Size() != 2 {
		t.Fatalf("intersection size should be 2, got %d", inter.Size())
	}
	if !inter.Contains(2) || !inter.Contains(3) {
		t.Fatal("intersection should contain 2 and 3")
	}
	if inter.Contains(1) || inter.Contains(4) {
		t.Fatal("intersection should not contain 1 or 4")
	}
}

// TestDifference verifies set difference.
func TestDifference(t *testing.T) {
	var a, b Set256
	a.Insert(1)
	a.Insert(2)
	a.Insert(3)
	b.Insert(2)
	diff := a.Difference(&b)
	if diff.Size() != 2 {
		t.Fatalf("difference size should be 2, got %d", diff.Size())
	}
	if !diff.Contains(1) || !diff.Contains(3) {
		t.Fatal("difference should contain 1 and 3")
	}
	if diff.Contains(2) {
		t.Fatal("difference should not contain 2")
	}
}

// TestSymmetricDifference verifies symmetric difference (XOR).
func TestSymmetricDifference(t *testing.T) {
	var a, b Set256
	a.Insert(1)
	a.Insert(2)
	b.Insert(2)
	b.Insert(3)
	sd := a.SymmetricDifference(&b)
	if sd.Size() != 2 {
		t.Fatalf("symmetric difference size should be 2, got %d", sd.Size())
	}
	if !sd.Contains(1) || !sd.Contains(3) {
		t.Fatal("symmetric difference should contain 1 and 3")
	}
	if sd.Contains(2) {
		t.Fatal("symmetric difference should not contain 2")
	}
}

// TestIsSubsetOf checks subset relationship.
func TestIsSubsetOf(t *testing.T) {
	var a, b Set256
	// empty is subset of anything
	if !a.IsSubsetOf(&b) {
		t.Fatal("empty should be subset of empty")
	}
	b.Insert(1)
	if !a.IsSubsetOf(&b) {
		t.Fatal("empty should be subset of non-empty")
	}
	a.Insert(1)
	if !a.IsSubsetOf(&b) {
		t.Fatal("{1} should be subset of {1}")
	}
	a.Insert(2)
	if a.IsSubsetOf(&b) {
		t.Fatal("{1,2} should not be subset of {1}")
	}
}

// TestEquals checks equality.
func TestEquals(t *testing.T) {
	var a, b Set256
	if !a.Equals(&b) {
		t.Fatal("two empty sets should be equal")
	}
	a.Insert(5)
	b.Insert(5)
	if !a.Equals(&b) {
		t.Fatal("same elements should be equal")
	}
	b.Insert(6)
	if a.Equals(&b) {
		t.Fatal("different sets should not be equal")
	}
}

// TestComplement checks complement operation.
func TestComplement(t *testing.T) {
	var s Set256
	c := s.Complement()
	if c.Size() != 256 {
		t.Fatalf("complement of empty should have 256 elements, got %d", c.Size())
	}
	// complement of full set should be empty
	cc := c.Complement()
	if !cc.IsEmpty() {
		t.Fatal("complement of full set should be empty")
	}
	// single element complement
	var single Set256
	single.Insert(42)
	comp := single.Complement()
	if comp.Size() != 255 {
		t.Fatalf("complement of single element should have 255 elements, got %d", comp.Size())
	}
	if comp.Contains(42) {
		t.Fatal("complement should not contain 42")
	}
	if !comp.Contains(41) || !comp.Contains(43) {
		t.Fatal("complement should contain 41 and 43")
	}
}

// TestNewFromSliceNil verifies that nil input produces an empty set.
func TestNewFromSliceNil(t *testing.T) {
	s := NewFromSlice(nil)
	if !s.IsEmpty() {
		t.Fatal("NewFromSlice(nil) should produce empty set")
	}
}

// TestNewFromSliceEmpty verifies that empty slice produces an empty set.
func TestNewFromSliceEmpty(t *testing.T) {
	s := NewFromSlice([]byte{})
	if !s.IsEmpty() {
		t.Fatal("NewFromSlice(empty) should produce empty set")
	}
}

// TestNewFromSliceSingleByte verifies a single-byte bitmap.
func TestNewFromSliceSingleByte(t *testing.T) {
	// 0b00001010 = bits 1 and 3 set → elements 1 and 3
	s := NewFromSlice([]byte{0x0a})
	if s.Size() != 2 {
		t.Fatalf("expected size 2, got %d", s.Size())
	}
	if !s.Contains(1) || !s.Contains(3) {
		t.Fatal("should contain 1 and 3")
	}
	if s.Contains(0) || s.Contains(2) {
		t.Fatal("should not contain 0 or 2")
	}
}

// TestBytesEmpty verifies that empty set serializes to nil.
func TestBytesEmpty(t *testing.T) {
	var s Set256
	if s.Bytes() != nil {
		t.Fatal("empty set Bytes() should be nil")
	}
}

// TestBytesTrimming verifies trailing zero trimming in Bytes().
func TestBytesTrimming(t *testing.T) {
	var s Set256
	// elements 1 and 3 → byte[0] = 0b00001010 = 0x0a, rest zero
	s.Insert(1)
	s.Insert(3)
	b := s.Bytes()
	if len(b) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(b))
	}
	if b[0] != 0x0a {
		t.Fatalf("expected 0x0a, got 0x%02x", b[0])
	}
}

// TestBytesHighBit verifies that element 255 needs all 32 bytes.
func TestBytesHighBit(t *testing.T) {
	var s Set256
	s.Insert(255)
	b := s.Bytes()
	if len(b) != 32 {
		t.Fatalf("element 255 should need 32 bytes, got %d", len(b))
	}
	// byte[31] should have the high bit set: 255/8=31, 255%8=7, 1<<7=0x80
	if b[31] != 0x80 {
		t.Fatalf("expected byte[31]=0x80, got 0x%02x", b[31])
	}
	// all other bytes should be zero
	for i := 0; i < 31; i++ {
		if b[i] != 0 {
			t.Fatalf("expected byte[%d]=0, got 0x%02x", i, b[i])
		}
	}
}

// TestRoundTrip verifies NewFromSlice ↔ Bytes round-trip.
func TestRoundTrip(t *testing.T) {
	var s Set256
	vals := []byte{0, 7, 8, 15, 16, 100, 200, 255}
	for _, v := range vals {
		s.Insert(v)
	}
	b := s.Bytes()
	s2 := NewFromSlice(b)
	if !s.Equals(&s2) {
		t.Fatal("round-trip should preserve the set")
	}
}

// TestRoundTripEmpty verifies empty set round-trip.
func TestRoundTripEmpty(t *testing.T) {
	var s Set256
	b := s.Bytes()
	s2 := NewFromSlice(b)
	if !s.Equals(&s2) {
		t.Fatal("empty round-trip should preserve empty set")
	}
}

// TestNewFromSliceTruncation verifies that slices longer than 32 bytes
// only use the first 32 bytes (copy semantics).
func TestNewFromSliceLong(t *testing.T) {
	data := make([]byte, 40)
	data[0] = 0xFF
	data[35] = 0xFF // beyond 32 bytes, should be ignored
	s := NewFromSlice(data)
	// byte[0] = 0xFF → elements 0..7
	if s.Size() != 8 {
		t.Fatalf("expected 8 elements from first byte, got %d", s.Size())
	}
}

// TestAllValues verifies insert/contains for every possible value.
func TestAllValues(t *testing.T) {
	var s Set256
	for i := 0; i < 256; i++ {
		s.Insert(byte(i))
	}
	for i := 0; i < 256; i++ {
		if !s.Contains(byte(i)) {
			t.Fatalf("full set should contain %d", i)
		}
	}
	if s.Size() != 256 {
		t.Fatalf("full set size should be 256, got %d", s.Size())
	}
	// remove all and verify
	for i := 0; i < 256; i++ {
		s.Remove(byte(i))
	}
	if !s.IsEmpty() {
		t.Fatal("set should be empty after removing all elements")
	}
}

// TestSetOperationsDoNotMutate verifies that union/intersection/difference
// do not modify the original sets.
func TestSetOperationsDoNotMutate(t *testing.T) {
	var a, b Set256
	a.Insert(1)
	b.Insert(2)
	aCopy := a.Clone()
	bCopy := b.Clone()

	a.Union(&b)
	a.Intersection(&b)
	a.Difference(&b)
	a.SymmetricDifference(&b)

	if !a.Equals(&aCopy) {
		t.Fatal("set operations should not mutate a")
	}
	if !b.Equals(&bCopy) {
		t.Fatal("set operations should not mutate b")
	}
}
