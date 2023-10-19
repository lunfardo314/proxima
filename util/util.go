package util

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/lunfardo314/proxima/util/set"
	"golang.org/x/exp/constraints"
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

func ForEachUniquePair[T any](sl []T, fun func(a1, a2 T) bool) {
	for i, r1 := range sl {
		for _, r2 := range sl[i+1:] {
			if !fun(r1, r2) {
				return
			}
		}
	}
}

func Keys[K comparable, V any](m map[K]V, filter ...func(k K) bool) []K {
	ret := make([]K, 0, len(m))
	if len(filter) == 0 {
		for k := range m {
			ret = append(ret, k)
		}
	} else {
		for k := range m {
			if filter[0](k) {
				ret = append(ret, k)
			}
		}
	}
	return ret
}

func HasKey[K comparable, V any](m map[K]V, k K) bool {
	_, yes := m[k]
	return yes
}

func KeysSorted[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) []K {
	ret := Keys(m)
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

// Values returns slice of values. Non-deterministic
func Values[K comparable, V any](m map[K]V) []V {
	ret := make([]V, 0, len(m))
	for _, v := range m {
		ret = append(ret, v)
	}
	return ret
}

func KeySet[K comparable, V any](m map[K]V, filter ...func(k K) bool) set.Set[K] {
	ret := set.New[K]()
	if len(filter) == 0 {
		for k := range m {
			ret.Insert(k)
		}
	} else {
		for k := range m {
			if filter[0](k) {
				ret.Insert(k)
			}
		}
	}
	return ret
}

func SortKeys[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) []K {
	ret := Keys(m)
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

func SortValues[K comparable, V any](m map[K]V, less func(v1, v2 V) bool) []V {
	ret := Values(m)
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

func MergeKeys[K comparable, V any](maps ...map[K]V) map[K]struct{} {
	ret := make(map[K]struct{}, 0)
	for _, m := range maps {
		for k := range m {
			ret[k] = struct{}{}
		}
	}
	return ret
}

func MinimumKey[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) K {
	var ret K
	if len(m) == 0 {
		return ret
	}
	for k := range m {
		ret = k
		break
	}
	for k := range m {
		if less(k, ret) {
			ret = k
		}
	}
	return ret
}

func MaximumKey[K comparable, V any](m map[K]V, less func(k1, k2 K) bool) K {
	return MaximumKey(m, func(k1, k2 K) bool {
		return less(k2, k1)
	})
}

func LargestCommonKey[K comparable, V any](m1, m2 map[K]V, less func(k1, k2 K) bool) (K, bool) {
	greaterOrEqual := func(k1, k2 K) bool {
		return !less(k1, k2)
	}
	ordered1 := SortKeys(m1, func(k1, k2 K) bool {
		return greaterOrEqual(k1, k2) // desc
	})
	var ret K
	var found bool
	for _, k := range ordered1 {
		if _, foundIn2 := m2[k]; foundIn2 {
			if greaterOrEqual(k, ret) {
				ret = k
				found = true
			}
		}
	}
	return ret, found
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

func CloneArglistShallow[T any](elems ...T) []T {
	ret := make([]T, len(elems))
	copy(ret, elems)
	return ret
}

func CloneMapShallow[K comparable, V any](m map[K]V) map[K]V {
	ret := make(map[K]V)
	for k, v := range m {
		ret[k] = v
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

func FindFirst[T any](slice []T, cond func(el T) bool) (T, bool) {
	for _, el := range slice {
		if cond(el) {
			return el, true
		}
	}
	var nilElem T
	return nilElem, false
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

func Sort[T any](slice []T, less func(i, j int) bool) []T {
	sort.Slice(slice, less)
	return slice
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
