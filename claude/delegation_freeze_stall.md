# Delegation freeze stall — why consensus health decays while participation stays full

> Status: **LIVE** — diagnosed 2026-08-26 on the testnet, fixed the same day,
> awaiting validation on a restarted fleet.

## 1. The symptom

Monitor, slot 64595:

| Figure | Value |
|--------|-------|
| Consensus health (`coverage_delta / supply`) | 96.05 % |
| Capital participation (`(on sequencers + delegated) / supply`) | 99.84 % |
| Delegations | 45, of which **8 unfrozen** |

Health has decayed monotonically from 99.99 % on 08-18 to 95.97 % on 08-26,
with three small step-ups (08-20, 08-23, 08-25). It is a ratchet, not an event.

## 2. The gap is delegations, exactly

```
supply - coverage_delta        2_187_088_925_562   3.948 %
  unfrozen delegated capital   2_096_061_615_094   3.783 %   (8 delegations)
  non-chained capital             88_721_033_232   0.160 %   (4288 siglock + 8071 other-lock UTXOs)
  residual                         2_306_277_236           (mine chain + snapshot skew)
```

Only **frozen** delegated capital contributes to the coverage delta; the
`frozen_coverage` on the stem equals the frozen part of `delegated_capital`
to the mote. Participation counts a delegation whether frozen or not, health
does not — that is the whole of the 3.8 point spread. Mining dust is
0.16 % and is *not* the health story.

Unfrozen ones are the largest: 926 G, 303 G, 300 G, 281 G, 112 G, 110 G,
52 G, 37 G, 22 G. They cluster on `oloc1`, `ger1`, `oseq1` (3 each);
`hloc0`, `oloc2`, `boot` have none.

Ruled out: the per-epoch freeze cap (`MaxFrozenDelegations` = 300, 45
delegations total) and the coverage-contribution upper bound
(`enforce_freeze_bounds` is **false** on every sequencer, 89–98 % headroom).

## 3. Root cause — two permanent stalls in the delegation pool

Both are silent (no WARN is emitted) and both survive until the sequencer
process restarts. Neither is a ledger or consensus fault: the delegations are
perfectly valid, the sequencer simply stops offering to freeze them.

### 3.1 Stale settled entry after a foreign transition

`DelegationPool` entries are keyed by ChainID and are refreshed only by the
sequencer's **own** milestones:

- `mergeDiscovered` (the 30 s LRB rescan) skips any ChainID already known;
- `onNewOutput` (the push listener) returns early if the ChainID is known;
- `Reconcile` only inspects entries that are `pending`, or unconfirmed-and-Undef
  and aged out. A settled, confirmed entry is never re-read;
- `ApplyMilestone` walks only the own-milestone chain.

So when the delegation's **master** transitions the output with a
non-sequencer transaction, the pool keeps the old `outputID` forever. In
`insertDelegations` the mandatory objective read then fails and the loop
`continue`s with no log:

```go
owid, err := p.StateReader().GetOutputWithID(d.outputID)
if err != nil || owid == nil { continue }   // silent, forever
```

**This is the miner behaviour.** Miners periodically sweep mined rewards into
their delegation *and re-point it at a different sequencer* in one
owner-signed transaction, which resets `delegateLockState` to `undef`:

- chain `fe49039f…`, tx `64506-25` (non-seq, holder `9a555fe2…`): delegation
  moved **hloc0 → oseq1**, topped up by 3 750 009 138 motes = 10 mining
  rewards.
- chain `3fe8ce83…`, tx `55435-96` (non-seq, holder `fb9fb14a…`): moved
  **ger1 → oloc1**, topped up by 3.75 G, inputs including two 374 990 000-mote
  mine rewards.

If the destination sequencer has ever hosted that chain before, its stale
entry blocks re-enrolment permanently. Confirmed on `oloc1`:

| Chain | Last FREEZE logged by oloc1 | Consumed outputID then | Current outputID | Age of stall |
|-------|------------------------------|------------------------|------------------|--------------|
| `3fe8ce83…` | 08-20 20:06 | `18605-85` | `55435-96` | ~25 h |
| `0cf0c459…` | 08-22 01:55 | `29083-3`  | `64421-…`  | ~4 h at the time of writing |

`FREEZE failed` count in the oseq1 and oloc1 logs: **0**. The freeze is never
attempted at all.

### 3.2 Pending freeze with no age-out when the milestone is orphaned

`Reconcile`, pending branch:

```go
case isDlg && dOut.State != Undef: settle from LRB
case isDlg:                        // still Undef -> keep pending
default:                           a.drop = s.stale
```

The `stale` age-out is consulted **only** when the delegation is absent from
the LRB. If the milestone carrying the freeze is orphaned, the delegation is
present-and-Undef, so the entry stays `pending` indefinitely — and `Snapshot`
excludes pending entries from `candidates`, so it is never retried.

Confirmed: `oloc1` logged `FREEZE delegation 52d19d5d… oid = 62793-69` at
08-26 01:48. Five hours later the chain's current output was still
`62793-69-02a0807c09c5…#0`, `state=undef`, produced by a non-sequencer tx.
The freeze never settled and was never re-attempted.

### 3.3 Side effect — phantom load

A stale entry left in state `Frozen` keeps contributing to `loadByEpoch`, so
the amount-weighted balancer in `selectDelegationsToFreeze` spreads against a
load vector that includes capital the sequencer no longer holds.

## 4. Mining dust — separate issue, negligible for health

Each mining transaction produces three outputs: the mine-chain successor, a
374 990 000-mote sigLock reward, and a **10 000-mote tag-along to `oseq1`**.
13 474 mine transactions so far, and the census shows 12 411 UTXOs, of which
4 288 siglocks (88.7 G motes) and 8 071 other-lock (mostly tag-along dust,
~130 M motes).

Recent mining tag-alongs *are* being drained (spot-checked at slots 64514,
64537, 64540, 64547, 64550 — all consumed). The ~8 000 orphans are historical:
the tag-along backlog is memDAG-driven and in-memory only, never rebuilt from
the state, so anything missed during a restart, a load-shed drop
(`nonseq_drop` is 24 k–34 k on every node) or a TTL expiry is stranded in the
state permanently. 130 M motes is 0.0000024 of supply — it is state bloat,
not a coverage problem.

The 4 288 idle sigLock rewards (0.16 % of supply) are the participation gap:
114 controllers hold UTXOs, only 6 delegate. Miners that do not delegate leave
their rewards idle.

## 5. Fixes — implemented 2026-08-26

1. **Discovery is now authoritative for entries with no pending transition.**
   `mergeDiscovered` takes the scanned branch's slot and, besides adding
   unknown delegations, *refreshes* a known entry whose outputID the LRB has
   moved past, and *drops* one absent from the scan. Cures 3.1 within one 30 s
   discovery cycle at no extra cost — `discoverFromLRB` already reads the
   objective state, and `IterateDelegatedOutputs` is exhaustive, which is what
   makes the drop safe. The drop also clears the phantom load of 3.3.

   Two guards keep it conservative: an entry with `pending != nil` is never
   touched (the LRB legitimately lags a freeze until the next branch), and the
   drop requires the entry to be confirmed *and* older than the scanned branch,
   so an older scan cannot undo a newer enrolment.

2. **A stuck pending freeze now ages out.** `Reconcile`'s settle condition
   became `isDlg && (dOut.State != Undef || s.stale)`: a pending entry still
   Undef long after the freeze was issued means the milestone carrying it was
   orphaned, and settling it back to the LRB is the only thing that returns it
   to the candidate set. Cures 3.2.

3. **The silent skips now warn.** Both `continue`s after the objective read in
   `insertDelegations` log at topic `tag_along` level 1 — the same level the
   neighbouring "temporarily skipped" tag-along messages use, since with fix 1
   in place a dangling outputID is transient.

Tests: `sequencer/delegationpool/discovery_test.go` was rewritten for the new
contract (refresh, drop, the two guards). `go test -race ./sequencer/...`,
`TestDelegationInflationMinimal` and `Test1SequencerPrunerIdle` pass.
`Test3Seq1TagAlong` flakes ~1 run in 3 **on the base commit too** — a
pre-existing timing flake in the spam assertion, and that test has no
delegations, so none of this code is reachable in it.

Not yet done: the fixes are unvalidated under live load. The stalled entries on
the running fleet are in process memory, so they persist until each sequencer
restarts on the new build. Do not restart under load — see the fork risk in the
operating notes.
