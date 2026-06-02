// Package smallkv is a small byte-keyed persistent map used by
// sequencer-request encodings (and other small per-tx parameter
// bundles). Wire format: a sorted tuple of [key || value] entries.
package smallkv

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"maps"
	"sort"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/util/lines"
)

// MaxEntries caps the number of entries in the map (also the
// max-elements bound passed to the tuples decoder).
const MaxEntries = 256

// sortedKeys returns the byte keys of m in ascending order. Inlined
// here (rather than reaching for proxima/util.KeysSorted) so the
// smallkv package doesn't pull proxima/util — which transitively
// drags x/text into the TinyGo wasm wallet build.
func sortedKeys(m map[byte][]byte) []byte {
	ret := make([]byte, 0, len(m))
	for k := range m {
		ret = append(ret, k)
	}
	sort.Slice(ret, func(i, j int) bool { return ret[i] < ret[j] })
	return ret
}

// Map is a small byte-keyed persistent map.
type Map struct {
	m map[byte][]byte
}

// New returns an empty Map.
func New() Map {
	return Map{m: make(map[byte][]byte)}
}

// Clone returns a deep copy of m.
func (m *Map) Clone() Map {
	return Map{m: maps.Clone(m.m)}
}

// Set writes v at key k. An empty v deletes the entry.
func (m *Map) Set(k byte, v []byte) {
	if len(v) == 0 {
		delete(m.m, k)
	} else {
		m.m[k] = bytes.Clone(v)
	}
}

// Get returns the value at k, or nil if not present.
func (m *Map) Get(k byte) []byte {
	return m.m[k]
}

// Len returns the number of entries in the map.
func (m *Map) Len() int {
	return len(m.m)
}

// Bytes serialises the map as a sorted tuple of [key || value] entries.
func (m *Map) Bytes() []byte {
	arr := tuples.EmptyTupleEditable(MaxEntries)
	for _, k := range sortedKeys(m.m) {
		easyfl_util.Assertf(len(m.m[k]) > 0, "len(m.m[k])>0")
		arr.MustPush(easyfl_util.Concat(k, m.m[k]))
	}
	return arr.Bytes()
}

// FromBytes deserialises a Map. Each entry must be at least 2 bytes
// (1-byte key + non-empty value); empty input returns an empty Map.
func FromBytes(data []byte) (Map, error) {
	arr, err := tuples.TupleFromBytes(data, MaxEntries)
	if err != nil {
		return Map{}, fmt.Errorf("smallkv.FromBytes: %w", err)
	}
	ret := New()
	arr.ForEach(func(i int, data []byte) bool {
		if len(data) <= 1 {
			err = fmt.Errorf("smallkv.FromBytes: invalid data: %s", easyfl_util.Fmt(data))
			return false
		}
		ret.Set(data[0], data[1:])
		return true
	})
	if err != nil {
		return Map{}, err
	}
	return ret, nil
}

// Lines pretty-prints the map for debugging.
func (m *Map) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	for _, k := range sortedKeys(m.m) {
		ln.Add("'%d': %s", k, hex.EncodeToString(m.Get(k)))
	}
	return ln
}
