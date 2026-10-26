# Attacher cap starvation — a replayed old milestone made a node deaf for four minutes

> **RESOLVED** — the node recovered on its own after 25 slots and the network
> never stalled. Both weaknesses behind it were fixed on `develop` on 2026-10-26
> (section 5, no ledger change), guarded by
> `tests/attacher_cap_starvation_test.go`. Investigated from an evidence package
> sent by the operator of the MORA, Centauri, Barnard and Luhmann sequencers,
> checked against the logs of our five testnet boxes.

## 1. The symptom

On 2026-10-25, 18:06:50–18:10:56 UTC, slots 63131–63155, one public node running
four sequencers (MORA, Centauri, Barnard, Luhmann) in one process stopped seeing
the rest of the network. Its LRB stayed at 63130 and fell 25 slots behind. Every
branch its sequencers proposed was refused as unhealthy, with a coverage delta of
about 0.56 T against a 65.5 T supply, and from 63133 they issued bootstrap
transactions on the 63130 baseline. At 18:10:46 the node recovered within one slot.

The rest of the network carried on. Our five sequencers and access nodes showed no
LRB lag and no stale attachers; our branches had less coverage for those slots,
because the MORA group's stake was missing from them.

The node runs a modified build. The operator states that `core/memdag/memdag.go`,
`core/vertex/vid.go`, `core/attacher/attach.go` and
`core/attacher/attacher_milestone.go` are byte-identical to v1.3.1p1 (`f2919ea2`),
and the counters and log lines match stock code.

## 2. Timeline (UTC)

| Time | Slot | Event |
|------|------|-------|
| 17:52:21 | 63047 | Our five sequencers submit their last milestones before a restart. |
| 18:04:49 | 63119 | On the MORA node a memDAG GC pass deletes 4,162 entries; the oldest slot held in the memDAG becomes 63047 and stays there. |
| 18:06:37.342 | 63130 | An 83-slot-old milestone, `s63048-19-00ee0c592ad6`, arrives by gossip. On our oloc2 it starts a re-attach cascade of about 220 old milestones of slots 63047–63048, all resolved within seconds. |
| 18:06:50 | 63131 | On the MORA node 94 attachers start for slot-63047 milestones whose inputs (also slot 63047) stay undefined. The `att` counter goes from 0 to 94; `seq_drop` starts climbing. |
| 18:06:50–18:10:46 | 63131–63155 | `seq_drop` rises from 0 to 1,480: every new sequencer transaction is refused by the input gate. |
| 18:10:46.200 | 63155 | The 94 attachers reach the memDAG TTL (24 slots after their vertices were added) and are abandoned (`stale attacher abandoned`). `att` is 0 at 18:10:46.234. |
| 18:10:50.312 | 63155 | GC deletes 9,558 unreferenced entries; detached vertices in the map drop from about 9,200 to 886. |
| 18:10:56 | 63156 | LRB one slot behind, normal again. |

## 3. Mechanism

Three steps, all in stock code:

1. **The replay is admitted.** A sequencer transaction arriving by gossip passes the
   dedup gate once its txid has been forgotten. `shouldAttachSequencer`
   (`core/core_modules/txinput_queue/txinput_queue.go`) then admits it even at the
   attacher cap, because it is older than the latest attached timestamp: the
   anti-starvation rule is meant to let a lagging node catch up.
2. **The re-attach cascade bypasses the cap.** `AttachTransaction`
   (`core/attacher/attach.go`) finds the vertex still in the memDAG in detached
   form and re-attaches it in place. Its attacher meets more detached milestones in
   the past cone, and `onDetachedVertex` re-attaches each with
   `go AttachTransaction(...)` (`core/attacher/attacher_milestone.go`), without
   asking the input gate. One gossiped transaction can start an attacher for every
   retained milestone in its past cone.
3. **Stuck attachers deafen the node.** When some of those milestones cannot
   solidify, their attachers stay until the stale-attacher guard abandons them,
   one memDAG TTL (24 slots) after their vertex was added. The guard counts from
   when the vertex entered the memDAG, not from the transaction's own timestamp,
   so a milestone 84 slots old still gets the full 24 slots. While they hold the
   cap, `shouldAttachSequencer` drops every sequencer transaction newer than the
   latest attached one, which is every live milestone and branch.

Steps 1 and 2 happened on our oloc2 too, at the same millisecond, and did no
harm: the cascade resolved. Step 3 needs the cascade to hit milestones that cannot
be solidified.

## 4. Why that node and not ours

The MORA node was holding far more detached vertices than ours: about 12,500 to
13,800 before 18:04:49 and about 8,000 during the stall, against about 3,100 on
oloc2. Its oldest held slot was pinned at exactly 63047 for six minutes before
the replay. A large set of old, retained detached milestones whose inputs had
already been deleted is exactly what gives the cascade milestones it cannot
solidify. The operator's own patch, written for a separate overnight failure,
says that their sequencer coordination kept references to old vertices in
expired plans. That retention is the likely reason, but it is theirs to confirm.

Our other four boxes did not re-attach at all: either the replay did not reach
them, or they no longer held the vertex.

## 5. The fixes

The node only needed to retain some old detached milestones and to receive an old
milestone again, and replaying an old signed milestone is free to anyone. Two
changes, both in the input gate and the attacher count, described in
`core/resilience.md` §7 and §8:

- **Old sequencer gossip is dropped.** `TxInputQueue.fromPeer` refuses a sequencer
  transaction older than the memDAG TTL by the ID that comes with the message,
  unless it was pulled, before dedup, persist and gossip (`old_seq_drop`). A replay
  stops at the first node. `TestReplayedOldMilestoneDropped` gossips a retained,
  detached old milestone and checks it stays detached, and that the same milestone
  passes once pulled.
- **Attachers working on the past do not hold the cap.** `core/attacher` counts the
  attachers whose milestone was already past the memDAG TTL when they started
  (`NumPastAttachers`, `att_past`), and outside forward sync
  `shouldAttachSequencer` leaves them out of the count compared with the cap.
  `TestPastAttachersDoNotStarveSequencerGossip` fills the cap with unsolvable old
  milestones and checks that a newer one still gets in; before the fix it was
  dropped until the TTL released the cap.

Rejected: counting the stale-attacher TTL from the transaction's own timestamp. It
would abandon the 94 at once, but also any legitimate catch-up more than 24 slots
behind.

Still open:

- **Why oloc2 still held slot-63047 milestones 83 slots later** is not explained.
  Stock nodes retain some detached vertices too.
- **Who replayed `s63048-19-00ee0c592ad6`** is not known.
- **A milestone breaking the cross-slot rule crashes the attacher.**
  `Transaction.BaselineDirection` asserts that a non-branch milestone with a
  cross-slot chain predecessor and no explicit baseline has an endorsement. The
  ledger enforces that only in full validation (`_crossSlotPredecessorCase` in
  `ledger/def/sequencer.easyfl`), which runs after the attacher has called it, so
  such a milestone passes the gossip checks and panics the node. Found while
  writing the test; it needs a sequencer controller key to sign, and is not
  fixed.
