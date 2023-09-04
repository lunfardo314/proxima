package util

import (
	"sort"

	"github.com/lunfardo314/proxima/util/set"
)

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

func ValuesByKeys[K comparable, V any](m map[K]V, keys []K) []V {
	ret := make([]V, 0, len(keys))
	for _, k := range keys {
		ret = append(ret, m[k])
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

func FilterSlice[T any](slice []T, filter func(el T) bool) []T {
	ret := slice[:0]
	for _, el := range slice {
		if filter(el) {
			ret = append(ret, el)
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
