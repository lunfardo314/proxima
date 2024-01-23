package util

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"golang.org/x/exp/constraints"
	"golang.org/x/exp/maps"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

type Integer interface {
	int | uint16 | uint32 | uint64 | int16 | int32 | int64
}

var prn = message.NewPrinter(language.English)

func GoThousands[T Integer](v T) string {
	return strings.Replace(prn.Sprintf("%d", v), ",", "_", -1)
}

func GoThousandsLazy[T Integer](v T) func() string {
	return func() string {
		return GoThousands(v)
	}
}

func ForEachUniquePair[T any](sl []T, fun func(a1, a2 T) bool) {
	for i, r1 := range sl {
		for _, r2 := range sl[i+1:] {
			if !fun(r1, r2) {
				return
			}
		}
	}
}

func KeysFiltered[K comparable, V any](m map[K]V, filter func(k K) bool) []K {
	ret := make([]K, 0, len(m))
	for k := range m {
		if filter(k) {
			ret = append(ret, k)
		}
	}
	return ret
}

func ValuesFiltered[K comparable, V any](m map[K]V, filter func(v V) bool) []V {
	ret := make([]V, 0, len(m))
	for _, v := range m {
		if filter(v) {
			ret = append(ret, v)
		}
	}
	return ret
}

func KeysSorted[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) []K {
	ret := maps.Keys(m)
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

func SortKeys[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) []K {
	ret := maps.Keys(m)
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

func List[T any](elems ...T) []T {
	return elems
}

func Maximum[T any](lst []T, less func(el1, el2 T) bool) T {
	var ret T
	first := true
	for _, el := range lst {
		if first {
			ret = el
			first = false
			continue
		}
		if less(ret, el) {
			ret = el
		}
	}
	return ret
}

func Minimum[T any](lst []T, less func(el1, el2 T) bool) T {
	var ret T
	first := true
	for _, el := range lst {
		if first {
			ret = el
			first = false
			continue
		}
		if less(el, ret) {
			ret = el
		}
	}
	return ret
}

func RangeReverse[T any](slice []T, fun func(i int, elem T) bool) {
	for i := len(slice) - 1; i >= 0; i-- {
		if !fun(i, slice[i]) {
			return
		}
	}
}

func FilterSlice[T any](slice []T, filter func(el T) bool, maxElems ...int) []T {
	if len(slice) == 0 {
		return slice
	}
	ret := slice[:0]
	for _, el := range slice {
		if filter(el) {
			ret = append(ret, el)
		}
		if len(maxElems) > 0 && len(ret) >= maxElems[0] {
			break
		}
	}
	var nilElem T
	for i := len(ret); i < len(slice); i++ {
		slice[i] = nilElem
	}
	return ret
}

func ClearSlice[T any](slice []T) []T {
	var nul T
	for i := range slice {
		slice[i] = nul
	}
	return slice[:0]
}

func EqualSlices[T comparable](s1, s2 []T) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
}

func AppendUnique[T comparable](slice []T, elems ...T) []T {
	slice = slices.Grow(slice, len(elems))
	for _, el := range elems {
		if slices.Index(slice, el) < 0 {
			slice = append(slice, el)
		}
	}
	return slice
}

func FindFirstKeyInMap[K comparable, V any](m map[K]V, cond ...func(k K) bool) (K, bool) {
	var fun func(k K) bool
	if len(cond) > 0 {
		fun = cond[0]
	} else {
		fun = func(_ K) bool { return true }
	}
	for k := range m {
		if fun(k) {
			return k, true
		}
	}
	var nilK K
	return nilK, false
}

func MustTakeFirstKeyInMap[K comparable, V any](m map[K]V) K {
	ret, ok := FindFirstKeyInMap(m)
	Assertf(ok, "MustTakeFirstKeyInMap: empty map")
	return ret
}

func MakeRange[T constraints.Integer](from, toIncl T) []T {
	Assertf(from <= toIncl, "from<=toIncl")
	ret := make([]T, toIncl-from+1)
	for i := range ret {
		ret[i] = from + T(i)
	}
	return ret
}

func Find[T comparable](lst []T, el T) int {
	for i, e := range lst {
		if e == el {
			return i
		}
	}
	return -1
}

func ED25519PrivateKeyFromHexString(str string) (ed25519.PrivateKey, error) {
	privateKey, err := hex.DecodeString(str)
	if err != nil {
		return nil, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("wrong private key size")
	}
	return privateKey, nil
}

func CloneExactCap(data []byte) []byte {
	ret := make([]byte, len(data))
	copy(ret, data)
	return ret
}

func MakeErrFuncForPrefix(prefix string) func(err interface{}, args ...interface{}) error {
	return func(err interface{}, args ...interface{}) error {
		if IsNil(err) {
			return nil
		}
		s := ""
		switch err := err.(type) {
		case string:
			s = fmt.Sprintf(err, args...)
		case interface{ Error() string }:
			s = fmt.Sprintf(err.Error(), args...)
		case interface{ String() string }:
			s = fmt.Sprintf(err.String(), args...)
		default:
			s = fmt.Sprintf("wrong error argument type: '%T'", err)
		}
		return fmt.Errorf("%s: '%s'", prefix, s)
	}
}

func DoUntil(bodyFun func(), cond func() bool) {
	for {
		bodyFun()
		if cond() {
			break
		}
	}
}

func PrivateKeyFromHexString(k string) (ed25519.PrivateKey, error) {
	bin, err := hex.DecodeString(k)
	if err != nil {
		return nil, err
	}
	if len(bin) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("PrivateKeyFromHexString: wrong data length")
	}
	return bin, nil
}

func MustPrivateKeyFromHexString(k string) ed25519.PrivateKey {
	ret, err := PrivateKeyFromHexString(k)
	AssertNoError(err)
	return ret
}

func Ref[T any](v T) *T {
	return &v
}

func Abs[T constraints.Integer](n T) T {
	if n < 0 {
		return -n
	}
	return n
}
