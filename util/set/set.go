package set

import "sort"

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
	return New[K]().AddAll(s)
}

// Contains nil-safe
func (s Set[K]) Contains(el K) bool {
	if s == nil {
		return false
	}
	_, contains := s[el]
	return contains
}

// AsList is non-deterministic
func (s Set[K]) AsList() []K {
	if s == nil {
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
