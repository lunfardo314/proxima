package set

import (
	"sort"

	"github.com/lunfardo314/proxima/util/lines"
)

type Set[K comparable] map[K]struct{}

func New[K comparable](elems ...K) Set[K] {
	ret := make(Set[K])
	ret.Insert(elems...)
	return ret
}

func (s Set[K]) Insert(elems ...K) Set[K] {
	for _, el := range elems {
		s[el] = struct{}{}
	}
	return s
}

func (s Set[K]) Remove(elems ...K) Set[K] {
	for _, el := range elems {
		delete(s, el)
	}
	return s
}

func (s Set[K]) IsEmpty() bool {
	return len(s) == 0
}

// ForEach nil-safe
func (s Set[K]) ForEach(fun func(el K) bool) {
	for el := range s {
		if !fun(el) {
			return
		}
	}
}

func (s Set[K]) AddAll(another Set[K]) Set[K] {
	another.ForEach(func(el K) bool {
		s.Insert(el)
		return true
	})
	return s
}

func (s Set[K]) Clone() Set[K] {
	if s == nil {
		return nil
	}
	return New[K]().AddAll(s)
}

// Contains nil-safe
func (s Set[K]) Contains(el K) bool {
	if len(s) == 0 {
		return false
	}
	_, contains := s[el]
	return contains
}

func (s Set[K]) ContainsAnyOf(elems ...K) bool {
	for i := range elems {
		if s.Contains(elems[i]) {
			return true
		}
	}
	return false
}

// AsList is non-deterministic
func (s Set[K]) AsList() []K {
	if len(s) == 0 {
		return nil
	}
	ret := make([]K, 0, len(s))
	s.ForEach(func(el K) bool {
		ret = append(ret, el)
		return true
	})
	return ret
}

func (s Set[K]) Ordered(less func(el1, el2 K) bool) []K {
	ret := s.AsList()
	sort.Slice(ret, func(i, j int) bool {
		return less(ret[i], ret[j])
	})
	return ret
}

func (s Set[K]) Maximum(less func(el1, el2 K) bool, suchAs ...func(el K) bool) (ret K) {
	if len(s) == 0 {
		return
	}
	first := true
	suchAsFun := func(_ K) bool { return true }
	if len(suchAs) > 0 {
		suchAsFun = suchAs[0]
	}
	s.ForEach(func(el K) bool {
		if !suchAsFun(el) {
			return true
		}
		if first {
			ret = el
			first = false
		}
		if less(ret, el) {
			ret = el
		}
		return true
	})
	return
}

func (s Set[K]) Minimum(less func(el1, el2 K) bool, suchAs ...func(el K) bool) (ret K) {
	return s.Maximum(func(el1, el2 K) bool {
		return !less(el1, el2)
	}, suchAs...)
}

func Union[K comparable](sets ...Set[K]) Set[K] {
	ret := New[K]()
	for _, s := range sets {
		ret.AddAll(s)
	}
	return ret
}

func Intersect[K comparable](sets ...Set[K]) Set[K] {
	ret := New[K]()
	var allContains bool
	Union(sets...).ForEach(func(el K) bool {
		allContains = true
		for _, s := range sets {
			if !s.Contains(el) {
				allContains = false
				break
			}
		}
		if allContains {
			ret.Insert(el)
		}
		return true
	})
	return ret
}

func (s Set[K]) Lines(toStr func(key K) string, prefix ...string) *lines.Lines {
	ret := lines.New(prefix...)
	s.ForEach(func(el K) bool {
		ret.Add(toStr(el))
		return true
	})
	return ret
}
