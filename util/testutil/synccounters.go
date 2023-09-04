package testutil

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
)

type SyncCounters struct {
	mutex sync.RWMutex

	m map[string]int
}

func NewSynCounters() *SyncCounters {
	return &SyncCounters{
		m: make(map[string]int),
	}
}

func (s *SyncCounters) Set(name string, i int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	s.m[name] = i
}

func (s *SyncCounters) Add(name string, i int) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	v := s.m[name]
	s.m[name] = v + i
}

func (s *SyncCounters) Inc(name string) {
	s.Add(name, 1)
}

func (s *SyncCounters) Value(name string) int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	return s.m[name]
}

func (s *SyncCounters) String() string {
	s.mutex.RLock()
	defer s.mutex.RUnlock()

	keys := make([]string, 0, len(s.m))
	for k := range s.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, k := range keys {
		buf.WriteString(fmt.Sprintf("%s: %d\n", k, s.m[k]))
	}
	return buf.String()
}

func (s *SyncCounters) CheckValues(expected map[string]int) error {
	for name, value := range expected {
		if s.Value(name) != value {
			return fmt.Errorf("counter '%s': expected: %d, got %d", name, value, s.Value(name))
		}
	}
	return nil
}
