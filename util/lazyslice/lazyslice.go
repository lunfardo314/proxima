package lazyslice

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"sync"

	"github.com/lunfardo314/easyfl"
	"github.com/lunfardo314/proxima/util"
	"github.com/lunfardo314/unitrie/common"
)

// Array can be interpreted two ways:
// - as byte slice
// - as serialized append-only array of up to 255 byte slices
// Serialization cached and optimized by analyzing maximum length of the data element
// if readOnly == false NOT THREAD_SAFE!!!
// if readOnly == true, it is thread safe

type Array struct {
	readonly       bool
	mutex          sync.RWMutex
	bytes          []byte
	parsed         [][]byte
	maxNumElements int
}

type lenPrefixType uint16

// prefix of the serialized slice array are two bytes interpreted as uint16
// The highest 2 bits are interpreted as 4 possible dataLenBytes (0, 1, 2 and 4 bytes)
// The rest is interpreted as uint16 of the number of elements in the array. Max 2^14-1 =
// 0 byte with ArrayMaxData code, the number of bits reserved for element data length
// 1 byte is number of elements in the array
const (
	dataLenBytes0  = uint16(0x00) << 14
	dataLenBytes8  = uint16(0x01) << 14
	dataLenBytes16 = uint16(0x02) << 14
	dataLenBytes32 = uint16(0x03) << 14

	dataLenMask  = uint16(0x03) << 14
	arrayLenMask = ^dataLenMask
	maxArrayLen  = int(arrayLenMask) // 16383

	emptyArrayPrefix = lenPrefixType(0)
)

func (dl lenPrefixType) dataLenBytes() int {
	m := uint16(dl) & dataLenMask
	switch m {
	case dataLenBytes0:
		return 0
	case dataLenBytes8:
		return 1
	case dataLenBytes16:
		return 2
	case dataLenBytes32:
		return 4
	}
	panic("very bad")
}

func (dl lenPrefixType) numElements() int {
	s := uint16(dl) & arrayLenMask
	return int(s)
}

func (dl lenPrefixType) Bytes() []byte {
	ret := make([]byte, 2)
	binary.BigEndian.PutUint16(ret, uint16(dl))
	return ret
}

func ArrayFromBytesReadOnly(data []byte, maxNumElements ...int) *Array {
	mx := maxArrayLen
	if len(maxNumElements) > 0 {
		mx = maxNumElements[0]
	}
	ret := &Array{
		bytes:          data,
		maxNumElements: mx,
	}
	ret.readonly = true
	return ret
}

func ParseArrayFromBytesReadOnly(data []byte, maxNumElements ...int) (*Array, error) {
	var ret *Array
	err := util.CatchPanicOrError(func() error {
		ret = ArrayFromBytesReadOnly(data, maxNumElements...)
		ret.NumElements()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

// EmptyArray by default mutable
func EmptyArray(maxNumElements ...int) *Array {
	return ArrayFromBytesReadOnly(emptyArrayPrefix.Bytes(), maxNumElements...).SetReadOnly(false)
}

func MakeArrayFromDataReadOnly(element ...[]byte) *Array {
	ret := EmptyArray(len(element))
	for _, el := range element {
		ret.Push(el)
	}
	return ret.SetReadOnly(true)
}

func MakeArrayReadOnly(element ...any) *Array {
	ret := EmptyArray(len(element))
	for _, el := range element {
		switch e := el.(type) {
		case []byte:
			ret.Push(e)
		case interface{ Bytes() []byte }:
			ret.Push(e.Bytes())
		default:
			panic("lazyarray.Make: only '[]byte' and 'interface{Bytes() []byte}' types are allowed as arguments")
		}
	}
	ret.SetReadOnly(true)
	return ret
}

func (a *Array) SetData(data []byte) {
	a.mustEditable()
	a.bytes = data
	a.parsed = nil
}

func (a *Array) SetEmptyArray() {
	a.mustEditable()
	a.SetData([]byte{0, 0})
}

func (a *Array) mustEditable() {
	if a.readonly {
		panic("attempt to write to a read only lazy array")
	}
}

func (a *Array) SetReadOnly(ro bool) *Array {
	a.readonly = ro
	return a
}

func (a *Array) Push(data []byte) int {
	a.mustEditable()
	a.ensureParsed()
	if len(a.parsed) >= a.maxNumElements {
		panic("Array.Push: too many elements")
	}
	a.parsed = append(a.parsed, data)
	a.bytes = nil // invalidate bytes
	return len(a.parsed) - 1
}

// PutAtIdx puts data at index, panics if array has no element at that index
func (a *Array) PutAtIdx(idx byte, data []byte) {
	a.mustEditable()
	a.parsed[idx] = data
	a.bytes = nil // invalidate bytes
}

// PutAtIdxGrow puts data at index, grows array of necessary
func (a *Array) PutAtIdxGrow(idx byte, data []byte) {
	a.mustEditable()
	for int(idx) >= a.NumElements() {
		a.Push(nil)
	}
	a.PutAtIdx(idx, data)
}

func (a *Array) ForEach(fun func(i int, data []byte) bool) {
	for i := 0; i < a.NumElements(); i++ {
		if !fun(i, a.At(i)) {
			break
		}
	}
}

func (a *Array) validBytes() bool {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	if len(a.bytes) > 0 {
		return true
	}
	return len(a.parsed) == 0
}

func (a *Array) parsedNotNil() bool {
	a.mutex.RLock()
	defer a.mutex.RUnlock()

	return len(a.parsed) > 0
}

func (a *Array) ensureParsed() {
	if a.parsedNotNil() {
		return
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()

	if a.parsed != nil {
		return
	}
	var err error
	a.parsed, err = parseArray(a.bytes, a.maxNumElements)
	if err != nil {
		panic(err)
	}
}

func (a *Array) ensureBytes() {
	if a.validBytes() {
		return
	}
	var buf bytes.Buffer

	a.mutex.RLock()
	defer a.mutex.RUnlock()

	if err := encodeArray(a.parsed, &buf); err != nil {
		panic(err)
	}
	a.bytes = buf.Bytes()
}

func (a *Array) At(idx int) []byte {
	a.ensureParsed()
	return a.parsed[idx]
}

func (a *Array) AtSafe(idx int) ([]byte, error) {
	var ret []byte
	err := util.CatchPanicOrError(func() error {
		ret = a.At(idx)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (a *Array) Parsed() [][]byte {
	a.ensureParsed()
	return a.parsed
}

func (a *Array) ParsedString() string {
	p := a.Parsed()
	ret := make([]string, len(p))
	for i := range p {
		ret[i] = easyfl.Fmt(p[i])
	}
	return fmt.Sprintf("[%s]", strings.Join(ret, ","))
}

func (a *Array) NumElements() int {
	a.ensureParsed()
	return len(a.parsed)
}

func (a *Array) Bytes() []byte {
	a.ensureBytes()
	return a.bytes
}

func (a *Array) AsTree() *Tree {
	return &Tree{
		sa:       a,
		subtrees: make(map[byte]*Tree),
	}
}

func calcLenPrefix(data [][]byte) (lenPrefixType, error) {
	if len(data) > maxArrayLen {
		return 0, errors.New("too long data")
	}
	if len(data) == 0 {
		return emptyArrayPrefix, nil
	}
	var dl uint16
	var t uint16
	for _, d := range data {
		t = dataLenBytes0
		switch {
		case len(d) > math.MaxUint32:
			return 0, errors.New("data can't be longer that MaxInt32")
		case len(d) > math.MaxUint16:
			t = dataLenBytes32
		case len(d) > math.MaxUint8:
			t = dataLenBytes16
		case len(d) > 0:
			t = dataLenBytes8
		}
		if dl < t {
			dl = t
		}
	}
	return lenPrefixType(dl | uint16(len(data))), nil
}

func writeData(data [][]byte, numDataLenBytes int, w io.Writer) error {
	if numDataLenBytes == 0 {
		return nil // all empty
	}
	for _, d := range data {
		switch numDataLenBytes {
		case 1:
			if _, err := w.Write([]byte{byte(len(d))}); err != nil {
				return err
			}
		case 2:
			var b [2]byte
			binary.BigEndian.PutUint16(b[:], uint16(len(d)))
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
		case 4:
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], uint32(len(d)))
			if _, err := w.Write(b[:]); err != nil {
				return err
			}
		}
		if _, err := w.Write(d); err != nil {
			return err
		}
	}
	return nil
}

// decodeElement 'reads' element without memory allocation, just cutting a slice
// from the data. Suitable for immutable data
func decodeElement(buf []byte, numDataLenBytes int) ([]byte, []byte, error) {
	var sz int
	switch numDataLenBytes {
	case 0:
		sz = 0
	case 1:
		sz = int(buf[0])
	case 2:
		sz = int(binary.BigEndian.Uint16(buf[:2]))
	case 4:
		sz = int(binary.BigEndian.Uint32(buf[:4]))
	default:
		return nil, nil, errors.New("wrong lenPrefixType value")
	}
	return buf[numDataLenBytes+sz:], buf[numDataLenBytes : numDataLenBytes+sz], nil
}

// decodeData decodes by splitting into slices, reusing the same underlying array
func decodeData(data []byte, numDataLenBytes int, n int) ([][]byte, error) {
	ret := make([][]byte, n)
	var err error
	for i := 0; i < n; i++ {
		data, ret[i], err = decodeElement(data, numDataLenBytes)
		if err != nil {
			return nil, err
		}
	}
	if len(data) != 0 {
		return nil, errors.New("serialization error: not all bytes were consumed")
	}
	return ret, nil
}

func encodeArray(data [][]byte, w io.Writer) error {
	prefix, err := calcLenPrefix(data)
	if err != nil {
		return err
	}
	if _, err = w.Write(prefix.Bytes()); err != nil {
		return err
	}
	return writeData(data, prefix.dataLenBytes(), w)
}

func parseArray(data []byte, maxNumElements int) ([][]byte, error) {
	if len(data) < 2 {
		return nil, errors.New("unexpected EOF")
	}
	prefix := lenPrefixType(binary.BigEndian.Uint16(data[:2]))
	if prefix.numElements() > maxNumElements {
		return nil, fmt.Errorf("parseArray: number of elements in the prefix %d is larger than maxNumElements %d ",
			prefix.numElements(), maxNumElements)
	}
	return decodeData(data[2:], prefix.dataLenBytes(), prefix.numElements())
}

//------------------------------------------------------------------------------

// Tree is a read only interface to Array, which is interpreted as a tree
type Tree struct {
	// bytes
	sa *Array
	// cache of parsed subtrees
	subtrees     map[byte]*Tree
	subtreeMutex sync.RWMutex
}

type TreePath []byte

// MaxElementsLazyTree each node of the tree can have maximum 256 elements
const MaxElementsLazyTree = 256

func TreeFromBytesReadOnly(data []byte) *Tree {
	return &Tree{
		sa:       ArrayFromBytesReadOnly(data, MaxElementsLazyTree),
		subtrees: make(map[byte]*Tree),
	}
}

func TreeFromTreesReadOnly(trees ...*Tree) *Tree {
	util.Assertf(len(trees) <= MaxElementsLazyTree, "can't be more than %d tree node elements", MaxElementsLazyTree)

	sa := EmptyArray(MaxElementsLazyTree)
	m := make(map[byte]*Tree)
	for i, tr := range trees {
		sa.Push(tr.Bytes())
		m[byte(i)] = tr
	}

	return &Tree{
		sa:       sa.SetReadOnly(true),
		subtrees: m,
	}
}

func TreeEmpty() *Tree {
	return TreeFromBytesReadOnly(emptyArrayPrefix.Bytes())
}

func Path(p ...any) TreePath {
	return common.Concat(p...)
}

func (p TreePath) Bytes() []byte {
	return p
}

func (p TreePath) String() string {
	return fmt.Sprintf("%v", []byte(p))
}

func (p TreePath) Hex() string {
	return hex.EncodeToString(p.Bytes())
}

// Bytes recursively updates bytes in the tree from leaves
func (st *Tree) Bytes() []byte {
	if st.sa.validBytes() {
		return st.sa.Bytes()
	}
	st.subtreeMutex.RLock()
	defer st.subtreeMutex.RUnlock()

	for i, subtree := range st.subtrees {
		if !subtree.sa.validBytes() {
			st.sa.PutAtIdx(i, subtree.Bytes())
		}
	}
	return st.sa.Bytes()
}

// takes from cache or creates a subtree
func (st *Tree) getSubtree(idx byte) *Tree {
	st.subtreeMutex.RLock()
	defer st.subtreeMutex.RUnlock()

	ret, ok := st.subtrees[idx]
	if ok {
		return ret
	}
	return TreeFromBytesReadOnly(st.sa.At(int(idx)))
}

func (st *Tree) Subtree(path TreePath) *Tree {
	if len(path) == 0 {
		return st
	}
	subtree := st.getSubtree(path[0])
	ret := subtree.Subtree(path[1:])

	st.subtreeMutex.Lock()
	defer st.subtreeMutex.Unlock()

	st.subtrees[path[0]] = subtree
	return ret
}

// BytesAtPath returns serialized for of the element at globalpath
func (st *Tree) BytesAtPath(path TreePath) []byte {
	if len(path) == 0 {
		return st.Bytes()
	}
	if len(path) == 1 {
		return st.sa.At(int(path[0]))
	}
	subtree := st.getSubtree(path[0])
	ret := subtree.BytesAtPath(path[1:])

	st.subtreeMutex.Lock()
	defer st.subtreeMutex.Unlock()

	st.subtrees[path[0]] = subtree
	return ret
}

// NumElements returns number of elements of the Array at the end of globalpath
func (st *Tree) NumElements(path TreePath) int {
	return st.Subtree(path).sa.NumElements()
}

func (st *Tree) ForEach(fun func(i byte, data []byte) bool, path TreePath) {
	sub := st.Subtree(path)
	for i := 0; i < sub.sa.NumElements(); i++ {
		if !fun(byte(i), sub.sa.At(i)) {
			return
		}
	}
}

func (st *Tree) ForEachIndex(fun func(i byte) bool, path TreePath) {
	for i := 0; i < st.NumElements(path); i++ {
		if !fun(byte(i)) {
			break
		}
	}
}
