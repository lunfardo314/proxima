package mine

import (
	"testing"

	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/stretchr/testify/require"
)

// An upgrade can move the mining start slot past its own slot, pausing mining. The miner
// takes the start slot from the rules of the slot it mines for, not from the constants read
// at launch: before the upgrade it mines on, from the upgrade slot it targets the start slot.
func TestSuccessorSlotFollowsUpgradePause(t *testing.T) {
	f := newVerifyFixture(t, 6)
	m := &miner{lib: f.m.lib, consts: f.m.consts}
	now := m.nowSlot()
	upgrade, resume := now+10, now+1010

	paused := *m.consts
	paused.DisableMiningUntilSlot = resume
	m.slotConsts = map[uint32]*txbuildercore.Constants{}
	for s := now - 2; s < upgrade; s++ {
		m.slotConsts[s] = m.consts
	}
	for s := upgrade; s <= upgrade+4; s++ {
		m.slotConsts[s] = &paused
	}
	m.slotConsts[resume] = &paused

	// far from the upgrade: the next slot by the pace, or now
	got := m.successorSlot(now - 1)
	require.True(t, got == now || got == now+1, "got %d, now %d", got, now)
	// the first slot of the upgrade: moved to the start slot of its rules
	require.EqualValues(t, resume, m.successorSlot(upgrade-1))
}
