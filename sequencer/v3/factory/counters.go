package factory

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Counters counts named events of one slot for the SLOT STATS line: where the factory's pairs and
// skeletons go, and why the proposer drops them. A sequencer issuing nothing for a slot shows there
// which step produced nothing.
type Counters struct {
	mutex sync.Mutex
	m     map[string]int
}

func (c *Counters) Inc(key string) {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if c.m == nil {
		c.m = make(map[string]int)
	}
	c.m[key]++
}

func (c *Counters) Reset() {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	c.m = nil
}

// String lists the counters as "key=value", sorted by key, or "none".
func (c *Counters) String() string {
	c.mutex.Lock()
	defer c.mutex.Unlock()
	if len(c.m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(c.m))
	for k := range c.m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, c.m[k])
	}
	return strings.Join(parts, " ")
}
