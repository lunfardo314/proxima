//go:build !race

package tests

import "time"

// How long the multi-sequencer setup waits for the chain-origins transaction to
// reach the heaviest state; longer under the race detector (race_on_test.go).
const setupWaitTimeout = 30 * time.Second
