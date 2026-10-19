package mine

import (
	"github.com/lunfardo314/proxima/ledger/base"
	"github.com/lunfardo314/proxima/ledger/txbuildercore"
	"github.com/lunfardo314/proxima/proxi/glb"
)

// The miner across a library upgrade (kb/library_upgrade1.md). The rules a
// transit must follow are those of the library applying at its own slot: the
// miner version written into it, the transaction version (upgrade index) and,
// from version 5 on, the earliest tick it may be stamped at. A miner built for
// a later version mines the earlier rules until their last slot, so miners can
// update ahead of an upgrade and nothing has to happen at the upgrade itself.

// minMinerVersion is the oldest miner version this build still mines.
const minMinerVersion uint32 = 4

// minerVersionSettledTick is the first version whose transits are stamped late
// in their slot: from it on, the ledger refuses a transit stamped before the
// settlement tick minus the sequencer pace, so no sequencer can include it
// before the settlement window.
const minerVersionSettledTick uint32 = 5

// constsAt returns the ledger constants of the library applying at slot. They
// change only at an upgrade, so each slot is asked for once and kept for a
// while. Without an answer from the node, the constants read at start stand.
func (m *miner) constsAt(slot uint32) *txbuildercore.Constants {
	m.slotConstsMu.Lock()
	defer m.slotConstsMu.Unlock()

	if c, ok := m.slotConsts[slot]; ok {
		return c
	}
	if m.c == nil {
		return m.consts
	}
	c, err := m.c.GetLedgerConstants(&slot)
	if err != nil {
		glb.Verbosef("   cannot read the ledger constants of slot %d: %v", slot, err)
		return m.consts
	}
	if m.slotConsts == nil {
		m.slotConsts = make(map[uint32]*txbuildercore.Constants)
	}
	for s := range m.slotConsts {
		if s+slotConstsKeepSlots < slot {
			delete(m.slotConsts, s)
		}
	}
	m.slotConsts[slot] = c
	return c
}

// slotConstsKeepSlots is how long the constants of a slot are kept: past any
// slot a transit could still be built or received for.
const slotConstsKeepSlots = 64

// minerVersionSupported tells whether this build mines the rules of a version.
func minerVersionSupported(v uint32) bool {
	return v >= minMinerVersion && v <= MinerVersion
}

// transitTick is the tick a transit for a slot is stamped at: 1 under the old
// rules, the settlement tick minus the sequencer pace from version 5 on, the
// earliest the ledger accepts there.
func transitTick(c *txbuildercore.Constants) uint8 {
	if c.MinerVersion < minerVersionSettledTick {
		return 1
	}
	return c.MineSettlementTick() - c.TransactionPaceSequencer
}

// transitTime is the timestamp of a transit for slot under that slot's rules.
func (m *miner) transitTime(slot uint32) base.LedgerTime {
	return base.T(slot, transitTick(m.constsAt(slot)))
}
