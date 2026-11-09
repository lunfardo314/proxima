package sequencer

// MaxOwnTipAgeSlots is how many slots a sequencer's own latest milestone may lag the current slot and
// still be extended while no reliable branch includes it.
const MaxOwnTipAgeSlots = 3

// OwnTipStale tells whether a sequencer must not extend its own latest milestone of slot tipSlot at
// slot nowSlot, and should restart its chain from the output committed in the latest reliable branch.
// A sequencer that wakes up after a long stall would otherwise extend a tip no branch ever took, and
// every node would have to reattach the long-detached cone behind it.
func OwnTipStale(tipSlot, nowSlot uint32, inLRB func() bool) bool {
	return tipSlot+MaxOwnTipAgeSlots < nowSlot && !inLRB()
}
