package base

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"maps"

	"github.com/lunfardo314/easyfl/easyfl_util"
	"github.com/lunfardo314/easyfl/tuples"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/proxima/util/lines"
)

type SmallPersistentMap struct {
	m map[byte][]byte
}

func NewSmallPersistentMap() SmallPersistentMap {
	return SmallPersistentMap{
		m: make(map[byte][]byte),
	}
}

func (m *SmallPersistentMap) Clone() SmallPersistentMap {
	return SmallPersistentMap{
		m: maps.Clone(m.m),
	}

}

func (m *SmallPersistentMap) Set(k byte, v []byte) {
	if len(v) == 0 {
		delete(m.m, k)
	} else {
		m.m[k] = bytes.Clone(v)
	}
}

// Get nil means not found
func (m *SmallPersistentMap) Get(k byte) []byte {
	return m.m[k]
}

func (m *SmallPersistentMap) Len() int {
	return len(m.m)
}

func (m *SmallPersistentMap) Bytes() []byte {
	arr := tuples.EmptyTupleEditable(256)
	sorted := util.KeysSorted(m.m, func(k1, k2 byte) bool {
		return k1 < k2
	})
	for _, k := range sorted {
		easyfl_util.Assertf(len(m.m[k]) > 0, "len(m.m[k])>0")
		arr.MustPush(easyfl_util.Concat(k, m.m[k]))
	}
	return arr.Bytes()
}

func SmallPersistentMapFromBytes(data []byte) (SmallPersistentMap, error) {
	arr, err := tuples.TupleFromBytes(data, 256)
	if err != nil {
		return SmallPersistentMap{}, fmt.Errorf("SmallPersistentMapFromBytes: %w", err)
	}
	ret := NewSmallPersistentMap()
	arr.ForEach(func(i int, data []byte) bool {
		if len(data) <= 1 {
			err = fmt.Errorf("SmallPersistentMapFromBytes: invalid data: %s", easyfl_util.Fmt(data))
			return false
		}
		ret.Set(data[0], data[1:])
		return true
	})
	if err != nil {
		return SmallPersistentMap{}, err
	}
	return ret, nil
}

func (m *SmallPersistentMap) Lines(prefix ...string) *lines.Lines {
	ln := lines.New(prefix...)
	for _, k := range util.KeysSorted(m.m, func(k1, k2 byte) bool { return k1 < k2 }) {
		ln.Add("'%d': %s", k, hex.EncodeToString(m.Get(k)))
	}
	return ln
}
