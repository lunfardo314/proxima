package util

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"time"

	"golang.org/x/exp/constraints"
	"golang.org/x/exp/maps"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

type Integer interface {
	int | uint8 | uint16 | uint32 | uint64 | int8 | int16 | int32 | int64
}

var prn = message.NewPrinter(language.English)

const defaultThousandsSeparator = "_"

// Th makes string representation of the integer with thousands separator
func Th[T Integer](v T, separator ...string) string {
	var sep string
	if len(separator) > 0 {
		sep = separator[0]
	} else {
		sep = defaultThousandsSeparator
	}
	return strings.Replace(prn.Sprintf("%d", v), ",", sep, -1)
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

func KeysFiltered[K comparable, V any](m map[K]V, cond func(k K) bool) []K {
	return KeysFilteredByValues(m, func(k K, _ V) bool {
		return cond(k)
	})
}

func KeysFilteredByValues[K comparable, V any](m map[K]V, cond func(k K, v V) bool) []K {
	ret := make([]K, 0, len(m))
	for k, v := range m {
		if cond(k, v) {
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

func StringsLess(s1, s2 string) bool {
	return s1 < s2
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

// IndexOfMaximum slice cannot be empty
func IndexOfMaximum[T any](lst []T, less func(i1, i2 int) bool) int {
	Assertf(len(lst) > 0, "len(lst)>0")

	ret := 0
	for i := range lst {
		if i != 0 {
			if less(ret, i) {
				ret = i
			}
		}
	}
	return ret
}

// PurgeSlice filters elements on the same underlying array
// Element remains in the array only if 'filter' function returns true
// The elements of the array which are not covered by slice are nullified.
// This may be important when slice contains pointers, which, in turn, may lead to hidden
// memory leak because GC can't remove them
func PurgeSlice[T any](slice []T, filter func(el T) bool) []T {
	if len(slice) == 0 {
		return slice
	}
	ret := slice[:0]
	for _, el := range slice {
		if filter(el) {
			ret = append(ret, el)
		}
	}
	// please the GC
	var nul T
	for i := len(ret); i < len(slice); i++ {
		slice[i] = nul
	}
	return ret
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

// RandomElements selects n random elements from a slice, which has more elements than n
func RandomElements[T any](n int, elems ...T) []T {
	Assertf(n > 0, "n>0")

	switch {
	case n >= len(elems):
		return elems
	case n == 0:
		return nil
	case n == 1:
		return []T{elems[rand.Intn(len(elems))]}
	}
	perm := slices.Clone(elems)
	rand.Shuffle(len(perm), func(i, j int) {
		perm[i], perm[j] = perm[j], perm[i]
	})

	return slices.Clone(perm[:n])
}

func MustLastElement[T any](sl []T) T {
	return sl[len(sl)-1]
}

func MakeRange[T constraints.Integer](from, toIncl T) []T {
	Assertf(from <= toIncl, "from<=toIncl")
	ret := make([]T, toIncl-from+1)
	for i := range ret {
		ret[i] = from + T(i)
	}
	return ret
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

func Percent(n, d int) float32 {
	return (float32(n) * 100) / float32(d)
}

func PercentString(n, d int) string {
	return fmt.Sprintf("%.2f", Percent(n, d))
}

// CallWithTimeout calls fun. If it does not finish in timeout period, calls onTimeout
func CallWithTimeout(ctx context.Context, timeout time.Duration, fun, onTimeout func()) {
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, errors.New("timeout"))
	go func() {
		<-ctx.Done()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			onTimeout()
		}
	}()
	fun()
	cancel()
}
