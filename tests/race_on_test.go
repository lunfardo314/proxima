//go:build race

package tests

import "time"

// The race detector slows the multi-sequencer setup several times over, so the
// chain-origins transaction needs longer to reach the heaviest state.
const setupWaitTimeout = 90 * time.Second
