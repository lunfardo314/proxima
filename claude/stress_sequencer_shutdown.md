# Stress test: gradual sequencer shutdown and restart

Live testnet exercise, 2026-08-06. Goal: stop sequencers one by one until the
network stops producing branches, then restart them and confirm full recovery.

## Fleet

Five boxes, each running a sequencer plus an access node. Access node API on
`:8001` is publicly reachable; the sequencer API on `:8000` is firewalled.

| box | IP | sequencer service | seq name | contribution | % of supply |
|-----|----|-------------------|----------|--------------|-------------|
| hboot | 78.46.56.22 | `proxima-hboot` | boot | 30.03T (20.02 own + 10.01 frozen) | 29.56% |
| hloc0 | 65.21.170.230 | `proxima-hloc0` | hloc0 | 20.01T | 19.70% |
| oseq1 | 79.137.70.25 | `proxima-oseq1` | oseq1 | 20.01T | 19.70% |
| oloc2 | 51.254.47.76 | `proxima-oloc2` | oloc2 | 20.01T | 19.70% |
| oloc1 | 54.37.255.106 | `proxima-oloc1` | oloc1 | 10.01T | 9.85% |

A sequencer's coverage contribution is `tokenBalance + frozenCoverage[0]` of its
own sequencer output — the same quantity the attacher checks against the
per-sequencer lower bound. `boot` carries the 10.01T of frozen delegated
capital, which is why it is the single largest contributor.

Tooling for the run lives in `.internal/stress/` (gitignored): `probe.py` for a
one-shot fleet table, `watch.sh` for continuous sampling, `logscan.sh` for the
failure signatures.

## Where the network stops, derived before running

`healthy_coverage = 7/12` = 58.33% of total supply. Three separate gates key off
it, and only two exempt the bootstrap chain:

| gate | location | bootstrap exempt |
|------|----------|------------------|
| build — proposer refuses to finalize | `sequencer/task/proposer.go` | yes |
| submit — `decideSubmitMilestone` | `sequencer/sequencer.go` | **no** |
| attach — attacher rejects on receipt | `core/attacher/wrapup.go` | yes |

`FindLatestReliableBranch` only advances over healthy branches, so once live
contribution drops below 7/12 the LRB freezes even if some branch were produced.

Predicted drawdown, stopping smallest first:

| step | stopped | live coverage | healthy |
|------|---------|---------------|---------|
| 0 | — | 98.51% | yes |
| 1 | oloc1 | 88.66% | yes |
| 2 | + oseq1 | 68.96% | yes |
| 3 | + oloc2 | 49.26% | **no** |

## Observed

Steps 1 and 2 matched the prediction to two decimals (88.65%, 68.95%). All five
access nodes stayed in lockstep — same LRB, same coverage, `synced: true`
throughout, no warnings in any log.

Step 3 halted the network at slot 18533 (last branch produced: `s18532`), with
`coverageDelta = 50,042,655,201,408` = 49.26% of supply. Both survivors refused,
but at *different* layers:

```
hloc0: tryBranchProposal-hloc0[18533-0]: finalize failed: finalize[branch]:
       branch unhealthy — coverageDelta 50042655201408 below health threshold
boot:  WON'T SUBMIT BRANCH s18533-0-01f99fe173df... reason: insufficient
       coverage delta. cov.delta: 120_098_523_271_427/50_042_655_201_408
```

The stall itself was clean: LRB pinned at 18531, `current_slot` still advancing
on wall clock, `synced: false` on all five nodes, `memory_stress_level: 0`,
`pipeline_size` flat. No attacher pileup, no memDAG growth, no divergence. The
network stopped rather than broke.

## Finding: the bootstrap exemption is inert for a live bootstrap sequencer

`boot` is exempt at the build gate, so it *constructs* a branch every slot, and
is then blocked at the submit gate, which has no exemption — so it discards that
branch every slot. The build-path carve-out is therefore unreachable in the live
path; it only ever matters for a node *receiving* a bootstrap branch (the
attach-path exemption).

Whether this is a defect depends on the intent. If the exemption was meant to
let the bootstrap chain carry a coverage-starved network alone, it does not, and
the submit gate needs the same `seqID != base.BoostrapSequencerID` carve-out. If
the intended lever for restarting a starved network is the `health_relief`
window — which is what its own doc comment says, and which is a whole-network
decision rather than a unilateral one — then the current behaviour is correct
and the build/attach exemptions are the anomaly. The health threshold exists so
a minority cannot advance consensus alone, and a lone bootstrap sequencer at
29.56% is exactly such a minority, which argues for the second reading.

Either way the two paths disagree, and the wasted per-slot branch build is real.

## Finding 2 (headline): rejoining after a stall is unreliable — one node
## recovered, one wedged permanently

The network *did* recover, but not promptly and not for everyone. Of the two
sequencers restarted into the stall, `oseq1` rejoined after ~3.5 minutes and
`oloc2` never did.

Both initially rejected **every** live milestone from the two survivors:

```
ATTACH s18566-3-00e7385f7274.. (baseline: s18531-0-01b877b0d951..)
  -> BAD(conflicting branch endorsement s18532-0-01e9c1e4a9d8..)
```

The rule is `core/attacher/attacher.go` in `attachEndorsementDependency`: an
endorsed branch must equal the attacher's own baseline, else the vertex is set
to `Bad`. During the stall the survivors keep issuing milestones whose chain
predecessor is rooted at the pre-stall branch `s18531` but which endorse the
last branch `s18532-0`. A rejoining node resolves the baseline to `s18531` and
so rejects the endorsement.

`Bad` is terminal and per-txid, so the rejection is permanent, and it poisons
descendants — later milestones fail on the ancestor instead:

```
BAD(ValidateConstraints of s18532-14-00c6479fbd0a..: tx.SetFullContext:
    'InputLoaderByIndex: consumed output s18532-0-01e9c1e4a9d8..#0 is not available')
```

Consequence: the rejoining sequencer never sees the others' coverage, so its own
branch proposals carry only its own contribution —

```
tryBranchProposal-oseq1[18565-0]: finalize failed: branch unhealthy —
coverageDelta 20013433871958 below health threshold
```

20,013,433,871,958 is exactly `oseq1`'s own 19.70% — consensus-isolated.

### How `oseq1` escaped

It fell back to the bootstrap-transaction path, issuing a tx with an *explicit*
baseline instead of an inherited one:

```
SUBMIT BOOTSTRAP TX s18569-3-... baseline: s18532-0-01e9c1e4a9d8..
```

That re-anchored it onto the branch the network had agreed on, its coverage
re-entered the past cone, and branch production resumed at 68.95% (boot + hloc0
+ oseq1). First branch after the stall: `s18582`, at 13:00:31 — about 3.5
minutes and ~20 slots after `oseq1` started, and ~50 slots after the stall
began. So the bootstrap re-anchor *is* the working recovery mechanism, and the
network is not permanently deadlocked.

Two caveats. The recovered network branches only on **every second slot** —
`boot` alternates `SUBMIT BRANCH` (18582, 18584, 18586) with `WON'T SUBMIT
BRANCH ... cov.delta 50_042_700_043_872` (18581, 18583, 18585), because `oseq1`
emits its bootstrap tx only every other slot, so coverage crosses 7/12 only on
those slots. And it recovered to 3 of 5 sequencers; the other two are still out.

### `oloc2` did not escape

`oloc2`'s sequencer node is still wedged 8+ minutes after restart, with the
network healthy around it:

```
[sync] latest reliable branch is 56 slots behind from now, current slot: 18588,
       coverage: 140_111_736_140_039     <- the stale pre-stall value
[memstats] [att: 29, ...], pipeline: 270, vertices: 138, GC counter: 99
```

Its LRB is frozen at `s18532` while the network is at 18584+, and it has issued
**no proposals at all** — not even the bootstrap txs that rescued `oseq1`. The
backlog is growing rather than draining (pipeline 54 -> 270, attachers 11 -> 29,
memDAG 31 -> 138), so this is an accumulating wedge, not a slow catch-up. The
`oloc2-acc` access node on the same box is unaffected and tracks the network
normally.

The difference between the two: `oseq1` stopped at slot 18513, *before* the
18532 fork existed, and on restart logged `ensureSyncedIfNecessary: node ready
(on canonical lineage)`. `oloc2` was live through the fork, stopped at 18531,
and during catch-up committed `s18532-0-01e9c1e4a9d8` before the network had
settled. That is the state that does not recover.

This is the same failure class as the previously recorded catch-up wedge: a
branchless gap replayed by a (re)joining node wedges it via terminal `Bad`. Here
the gap was only ~13 slots, far shorter than previously assumed necessary.

### Suspected mechanism (not yet confirmed in code)

`solidifyBaselineUnwrapped`'s `Undefined` case adopts `a.providedBaseline` — the
floor propagated by `depAttachOpts` — as a predecessor's baseline when the floor
branch's state already knows that predecessor. A node rejoining while its LRB is
still the pre-stall branch therefore clamps the whole predecessor chain to
`s18531`, after which any endorsement of `s18532-0` conflicts. This is
consistent with every observation but has not been proven; the alternative is
that the baseline should simply be widened to the newest endorsed branch rather
than inherited from the chain predecessor.

## Finding 3: two branches at the same slot with identical coverage delta

Slot 18532 held `s18532-0-01164ec7bfdf` (hloc0) and `s18532-0-01e9c1e4a9d8`
(boot), both with coverage delta exactly 70,055,797,546,282. Nodes disagreed on
the LRB for several minutes (`oloc1` reported 18532 while the other four
reported 18531) before all five converged on boot's branch. Worth checking that
the tie-break in `FindLatestReliableBranch` (`util.IndexOfMaximum`, which returns
the first maximum and so depends on iteration order) is deterministic across
nodes.

## Finding 4: `synced: true` while 47 slots behind a dead network

After LRB advanced to 18532, all five access nodes report `synced: true` with
`current_slot - lrb_slot = 47` and no branches being produced. The sync flag is
not a usable liveness signal in this state.

## Finding 5: the wedged sequencer never starts, so it cannot use the escape hatch

`oloc2`'s sequencer process never got past its startup precondition. The last
`[SEQ:oloc2]` line in the log, from the moment of restart, is:

```
ensureSyncedIfNecessary: waiting until node is on the canonical lineage
and synced before starting sequencer...
```

It never advances. `oseq1`, restarted into the same network, cleared the same
gate in about two seconds (`node ready (on canonical lineage), starting
sequencer`) and went on to rescue itself with bootstrap transactions.

This closes the loop on Finding 2. The dependency chain is circular:

- the sequencer waits to be *synced and on the canonical lineage* before starting;
- becoming synced requires attaching the live milestones;
- every attach fails terminally — 159 attempts, all with the identical reason
  `BAD(conflicting branch endorsement s18532-0-...)`, still firing against
  current-slot (18594+) traffic long after the network moved on;
- the bootstrap-transaction re-anchor, which is what actually breaks the
  deadlock, is only reachable *after* the sequencer starts.

So the one mechanism that can rescue a coverage-starved node sits behind a gate
that the wedge itself holds shut. `oseq1` escaped only because it was never
wedged in the first place.

Direct evidence from the wedged node's own API (`localhost:8000`, not reachable
externally):

```json
{"synced": false, "current_slot": 18609, "lrb_slot": 18532,
 "per_sequencer": {"a5ad81b0924a..": {"synced": false,
   "latest_healthy_slot": 18532, "latest_committed_slot": 18532,
   "ledger_coverage": 0}}}
```

`pipeline_size` 380 and rising, `memory_stress_level` still 0.

## Excluded: fleet binary skew

`hboot` (both nodes) runs commit `9ddbb88dd860` (2026-08-03) while the other
four boxes run `3da32bc36cba` (2026-08-04). Since `boot` produced the branch
everything wedged on, this was checked as a possible confound and **ruled out**:
the range `9ddbb88..3da32bc` is two commits touching only docs, the proxi config
template, and test fixtures — no Go code. The fleet should still be aligned, but
it explains none of the behaviour above.

## Note on branch cadence after partial recovery

With three sequencers at 68.95%, the network branches on every *second* slot:
`boot` alternates `SUBMIT BRANCH` (18602, 18604, 18606, 18608) with
`WON'T SUBMIT BRANCH ... cov.delta 50_042_7xx_xxx_xxx` (18601, 18603, 18605,
18607). `oseq1` emits its re-anchoring bootstrap tx only every other slot, so
coverage clears 7/12 only on those slots. Steady but half-rate, and a monitor
thresholding on `current_slot - lrb_slot >= 4` false-alarms in this mode.

## Finding 5a: the wedge survives restart, and the signature is `baseline: N/A`

Restarting the wedged `oloc2` did not fix it. It advanced once — LRB 18532 ->
18570, attachers 29 -> 0 — and then wedged again at 18570, with `behind` growing
monotonically (54, 56, 58, 60 ...). It is not converging.

**State divergence is ruled out.** `oloc2`'s committed LRB is
`s18570-0-0120f52b3428`, which is a genuine network branch at that slot (boot's;
`hloc0` produced `s18570-0-01070c9e1f07` in the same slot). The node holds a
legitimate branch — it simply cannot advance past it.

The signature common to every failure, in both the pre- and post-restart
episodes, is that **the milestone attacher ends up with no baseline at all**:

```
ATTACH s18572-0-01ec0e283f4f.. (baseline: N/A) -> BAD(ValidateConstraints of
  s18570-14-00546768a449..: tx.SetFullContext: 'InputLoaderByIndex: consumed
  output s18570-0-0120f52b3428..#0 at index 0 is not available')
```

With no baseline there is no state to load inputs from, so `InputLoaderByIndex`
reports the branch's own sequencer output as unavailable even though that branch
is committed locally. The failure then cascades forward through the whole branch
lineage, each branch failing on its predecessor:

```
s18572-0 BAD (input not available)
  -> s18574-0 BAD (conflicting branch endorsement s18572-0)
    -> s18576-0 -> s18578-0 -> s18580-0 -> s18582-0 -> ... -> s18620-0
```

Before the restart the baseline resolved to a stale-but-non-nil `s18531`; after
the restart it resolves to nil. Both produce terminal `Bad`. So the earlier
"clamped to the pre-stall branch" hypothesis is at best half the story — the
common defect is baseline determination failing outright when replaying a gap
that contains same-slot branch forks.

Note the gap contains *several* such forks, not just the one at 18532: slot
18570 also holds two branches. With multiple sequencers this is normal and is
resolved by later branches, so the forks are not themselves the bug; the bug is
that a replaying node cannot establish a baseline across them.

`oloc2` is reproducibly wedged and is the artifact to debug offline: it re-enters
this state on every restart, with a healthy network around it. Recovering the
node itself most likely needs a snapshot restore.

## CORRECTION (supersedes Finding 2): no restarted sequencer ever rejoined

Later evidence overturns the "oseq1 recovered" reading recorded above. It did
not. Its own node API, 122 slots after the stall:

```json
{"synced": false, "current_slot": 18654, "lrb_slot": 18532,
 "per_sequencer": {"c1a95d110d0a..": {"synced": false,
   "latest_healthy_slot": 18532, "latest_committed_slot": 18532}}}
```

It is frozen at `s18532`, exactly like `oloc2`, and has been re-issuing a
near-identical bootstrap transaction every second slot ever since — same
baseline, same coverage, same inflation, only the timestamp changing:

```
SUBMIT BOOTSTRAP TX s18649-3-... baseline: s18532-0-01e9c1e4a9d8.., coverage: 20_013_432_269_502, inflation: 660_037
SUBMIT BOOTSTRAP TX s18651-3-... baseline: s18532-0-01e9c1e4a9d8.., coverage: 20_013_432_269_502, inflation: 660_037
SUBMIT BOOTSTRAP TX s18653-3-... baseline: s18532-0-01e9c1e4a9d8.., coverage: 20_013_432_269_502, inflation: 660_037
```

What actually happened at 13:00 is that `boot` and `hloc0` began *ingesting*
those bootstrap txs, which lifted **their** coverage delta over 7/12 on the
slots where one landed. That is why branching resumed at half rate, and why the
`WON'T SUBMIT` lines alternate. The network's apparent recovery was an artifact
of a wedged node spamming re-anchor transactions — not a sequencer rejoining.

`oloc1`, started last, wedged immediately with the same signature:

```
ATTACH s18654-0-01d4a17f710e.. (baseline: N/A) -> BAD(ValidateConstraints of
  s18644-14-00f4a875791d..: 'InputLoaderByIndex: consumed output
  s18644-0-01100ee30ac3..#0 at index 0 is not available')
```

### The actual result of this stress test

**Every sequencer that was stopped and restarted is permanently wedged. None
rejoined. Only the two that never stopped (`boot`, `hloc0`) still function.**
The failure is deterministic, not a race: three independent nodes, restarted at
three different times against three different network states (stalled,
recovering, healthy), all wedged with the same signature.

The health-threshold halt is therefore not the interesting failure — it is
correct and reversible in principle. The real defect is that **a sequencer
cannot rejoin after any stop that spans a branchless gap**, which makes the
coverage-starvation stall effectively unrecoverable without operator
intervention (snapshot restore) on every affected node.

### Candidate defect worth checking first

The `baseline == endorsed branch` case now recurs consistently, with the *same*
transaction id on both sides:

```
ATTACH s18651-15-000ac07cb9e2.. (baseline: s18532-0-01e9c1e4a9d8..)
  -> BAD(conflicting branch endorsement s18532-0-01e9c1e4a9d8..)
```

`attachEndorsementDependency` rejects when
`vidEndorsed.ID() != *a.pastCone.GetBaseline()`. Caveat: `logErrorStatusString`
reads the baseline at *logging* time, so the two may have differed at check
time. But note `PastCone.SetBaseline` writes to `pc.delta.baselineBranchID` when
a delta exists, while `GetBaseline` returns the outer `pc.baselineBranchID`
whenever it is non-nil — a read/write asymmetry that would make a freshly-set
baseline invisible to exactly this comparison. Worth confirming or excluding
before chasing anything else.

## The `SetBaseline`/`GetBaseline` asymmetry: real, but NOT the cause of this wedge

Checked against the code. The suspicion recorded above is **half right**, and the
half that is wrong matters.

### The asymmetry is real and reachable

`PastCone` embeds `*PastConeBase`, so `pc.baselineBranchID` is the outer base's
field. The two accessors disagree when a delta is open:

```go
func (pc *PastCone) SetBaseline(id *base.TransactionID) {
    if pc.delta == nil { pc.baselineBranchID = id } else { pc.delta.baselineBranchID = id }
}
func (pc *PastCone) GetBaseline() *base.TransactionID {
    if pc.baselineBranchID != nil { return pc.baselineBranchID }   // outer wins
    if pc.delta != nil { return pc.delta.baselineBranchID }
    return nil
}
```

`BeginDelta` seeds the delta from the current outer baseline
(`NewPastConeBase(pc.baselineBranchID)`), so the two start equal. Three cases:

- no delta — consistent;
- delta open, outer baseline nil — `Get` falls through to the delta, consistent;
- **delta open, outer baseline already set** — `Set(X)` writes the delta while
  `Get` keeps returning the stale outer value. The new baseline is invisible
  until `CommitDelta` copies it out.

The third case is reachable. `MergePastCone` performs a baseline **swap**
(`pc.SetBaseline(pcb.baselineBranchID)` on the `needsBaselineSwap` path), and it
is called from `attacher.go` during past-cone traversal — which the
`IncrementalAttacher` enters *inside* a delta:

```
InsertEndorsement -> BeginDelta -> insertEndorsement
  -> attachEndorsementDependency -> ... -> MergePastCone -> SetBaseline(X)   // into delta
```

after which `attachEndorsementDependency`'s own check
`vidEndorsed.ID() != *a.pastCone.GetBaseline()` reads the **stale** baseline —
precisely the shape that produces a spurious `conflicting branch endorsement`.
`InsertInput` opens a delta over the same traversal.

Same class, worth fixing together: `baselineKnowsTx` reads
`pc.baselineBranchID` **directly** rather than via `GetBaseline()`, so under a
delta it consults the pre-swap baseline unconditionally — not even the
outer-nil fallback applies.

### But it does not explain the testnet wedge

`BeginDelta` is called only from `attacher_incremental.go` (and tests). The
`ATTACH ... -> BAD(...)` lines come from `milestoneAttacher.logErrorStatusString`,
and the milestone attacher — the path that validates *incoming* transactions —
never opens a delta. So `pc.delta` is nil throughout, `Set` and `Get` both hit
the outer field, and the asymmetry cannot fire there.

Conclusion: the delta asymmetry is a genuine latent defect in the **sequencer's
proposal-building** path (it would surface as spurious endorsement rejections
while assembling a milestone, not as rejected inbound traffic), and it should be
fixed on its own merits. The cause of the observed wedge is still open, and the
`baseline: N/A` on the milestone attacher — a baseline that was never
successfully determined, rather than one that was overwritten — remains the
thing to chase.

## Root failure isolated (cause narrowed, not yet proven)

`baseline: N/A` turned out to be a red herring. `milestoneAttacher.run()` asserts
`GetBaseline() != nil` right after a successful `solidifyBaseline()`, so `N/A`
only means the failure happened *inside* baseline solidification — and there,
`solidifyBaselineUnwrapped`'s `Bad` case does:

```go
case vertex.Bad:
    a.setError(baselineDirection.GetError())   // inherits the ancestor's error verbatim
```

So most of the log lines are **inherited error text**, not the logged tx's own
verdict. That is why the identical `conflicting branch endorsement s18532-0-...`
string still appears on transactions in slot 18651, and why the printed baseline
sometimes equals the "conflicting" branch: those attachers never failed on their
own endorsements at all.

### The original failure

Walking the inheritance back reaches `s18532-14-00ffe999ccba` — hloc0's
milestone in slot 18532. Read raw from the txstore API:

- endorsements: exactly one, `s18532-0-01e9c1e4a9d8` (boot's branch, **same slot**)
- inputs: exactly one, `s18531-14-000836606121` (chain predecessor, slot 18531)

Trace it through `BaselineDirection()` (`ledger/transaction/tx.go`): no explicit
baseline; chain predecessor is cross-slot (18531 != 18532) so the same-slot rule
does not apply; not a branch tx; therefore it falls through to
**`endorsement[0]` = `s18532-0`**.

So the baseline *direction* is the very branch it endorses. And
`WrappedTx.BaselineBranch()` states "a branch is its own baseline" and returns
`vid.id` for a branch. The baseline should therefore resolve to `s18532-0`,
which equals the endorsement, and `attachEndorsementDependency`'s check
(`vidEndorsed.ID() != *a.pastCone.GetBaseline()`) should pass trivially.

On the rejoining nodes it resolved to **`s18531`** instead. That mismatch is the
original defect; everything else in this incident is its forward cone.

### Where to look

The invariant being violated is precise and worth asserting directly:

> for a milestone whose `BaselineDirection()` is a branch `B`, the resolved
> baseline must be `B` itself.

Only two paths in `solidifyBaselineUnwrapped` can return a branch *other* than
the direction, and both substitute a **floor** for the real baseline:

1. the `EarliestStateKnowsTransaction(baselineDirectionID)` early return, which
   adopts a retained-history floor branch;
2. the `Undefined` case's `a.providedBaseline` adoption, gated on
   `BranchKnowsTransaction(*a.providedBaseline, baselineDirectionID)`.

Related: `AttachTxID` with `WithBaselineFloor` pre-sets
`vid.SetBaselineBranchIDNoLock(options.baseline)` *before* the vid's own
solidification runs, and `BaselineBranch()` will hand that floor to any reader
that sees the vid Good without it having solidified itself. `depAttachOpts`
propagates the floor only for non-branch sequencer dependencies, so the branch
direction itself should be exempt — which is what makes the observed `s18531`
result anomalous and is the first thing to instrument.

This is as far as static reading goes; confirming which path fires needs a
`Tracef` on baseline resolution against the wedged node, which is still live and
reproduces on every restart.

## Restart with tracing: the wedge did NOT reproduce — gap size is not the trigger

The fleet was stopped cleanly, rebuilt on `8f7f7478` (with the `baseline` trace
tag) and restarted in order: all access nodes, then `hboot` + `hloc0`, then
`oseq1`, then `oloc2`. Databases were **not** wiped.

Result: **every node recovered.** `oseq1`, which had been frozen at s18532 and
emitting the same bootstrap transaction for 120 slots, came back producing real
branches (branch counter 2107 -> 2118). `oloc2`, committed at s18531 against a
network at ~18948, crossed a **~410 slot gap** and rejoined with zero errors:

```
synced: true, lrb_slot 18947, latest_committed_slot 18948
3744 baseline resolutions
0 earliest-state floor   0 provided-baseline floor
0 DID NOT RESOLVE TO ITSELF   0 endorsement CONFLICT   0 BAD attaches
```

It then cleared the gate it had previously been stuck on for 10+ minutes —
`ensureSyncedIfNecessary: node ready (on canonical lineage)` — in ten seconds,
issued **one** bootstrap tx anchored on the **current** branch (`s18948`, not a
stale one), and went straight to normal milestones and a branch of its own.

### What this rules out

The gap is not the trigger. 410 branchless-equivalent slots crossed cleanly
here, versus a wedge after ~13 slots during the incident. So the reproduction
condition is not "a stopped sequencer must replay a gap"; it is **rejoining a
network that is itself in the degraded state** — specifically one stalled below
the health threshold, whose head carries an unresolved same-slot branch fork
(the two s18532 branches with identical coverage delta) that no later branch
exists to settle. Once the lineage was settled, the identical node, DB and code
path handled a 30x larger gap without a single bad attach.

### Healthy-node control

On healthy nodes every resolution takes the Good path, branch directions resolve
to themselves, and neither floor substitution is ever used:

```
solidify s18942-14-00de1494c3d8..: direction s18942-0-0170251f4a29.. (isBranch=true), floor none
solidify s18942-14-00de1494c3d8..: RESOLVED  s18942-0-0170251f4a29.. via Good direction s18942-0-0170251f4a29..
endorsement OK: branch s18942-0-0170251f4a29.. == baseline
```

So the instrumentation is confirmed working and silent in the healthy case,
which makes it a clean detector: if the wedge recurs, whichever counter goes
non-zero identifies the path directly.

### Consequence for reproducing it again

The live specimen is gone. Re-creating it means re-running the down-leg — stop
sequencers until coverage drops below 7/12, let the stall leave an unresolved
same-slot fork, then restart one sequencer into that state with the `baseline`
tag enabled. That is now a cheap, well-understood procedure, and the tracing
will answer the open question on the first attempt.

Operationally the good news stands on its own: **a coverage-starved network with
multiple wedged sequencers was fully recovered by a coordinated cold restart,
with no database wipe, no snapshot restore and no `health_relief` window.**

## Trace-tag noise: `endorsement CONFLICT` on sequencer nodes is normal

With the fleet healthy at 5/5, the `baseline` tag shows a seq/access split in the
conflict counter:

```
hboot   conflict=363   hboot-acc   conflict=0
hloc0   conflict=383   hloc0-acc   conflict=0
oseq1   conflict=436   oseq1-acc   conflict=0
oloc2   conflict=258   oloc2-acc   conflict=0
oloc1   conflict=89    oloc1-acc   conflict=0
```

This is **not** a defect, and it does not support the shared-vid/IncrementalAttacher
theory. The attacher name gives it away — these are proposer attachers:

```
improve-s18986-0-0115b9a0556d..: endorsement CONFLICT: endorsed s18986-0-0115b9a0556d..
                                 != baseline s18986-0-019a66c27616..
```

Two *different* branches in the same slot 18986. The `improve` proposer holds one
as its baseline and tries endorsing the other; the endorsement is legitimately
incompatible, `InsertEndorsement` rolls back its delta and the candidate is
skipped. Access nodes score zero simply because they run no proposer. The split
is explained entirely by "only sequencer nodes propose", with no bug behind it.

Practical consequence: on a sequencer node the `baseline` tag is noisier than on
an access node — hundreds of benign conflict lines per hour. When hunting the
wedge, filter on `floor1`/`floor2`/`DID NOT RESOLVE TO ITSELF` and on
`ATTACH ... -> BAD(`, not on the raw conflict count.

The delta read/write asymmetry documented earlier remains a real latent defect on
its own merits, but nothing observed here is evidence of it firing.

## Final state

Fleet fully restored: 5/5 sequencers, coverage delta 98.51% of supply — identical
to the pre-test baseline. `oloc1`, the last to start and ~475 slots behind,
crossed the gap with 23,105 baseline resolutions, zero floor substitutions, zero
invariant breaks, zero bad attaches, and rejoined with **no bootstrap re-anchor at
all** — straight to normal milestones (`endorse: 1`, then `endorse: 4`).

The only `BAD(` attaches anywhere are historical, last seen 13:57:34 on `oseq1`
during the boot+hloc0-only startup window; none since. The `WON'T SUBMIT BRANCH`
/ `branch unhealthy` warnings in the same window are the expected consequence of
`boot`+`hloc0` alone summing to 49.26%, below the 7/12 threshold, until the third
sequencer joined.

---

# 2026-08-07: root cause, fixes, and the full cold-restart run

## Root cause of the wedge

The stress test was repeated with the `baseline` tag live. The wedge reproduced on
the first restart and the traces identified the defect exactly.

`baseline: N/A` in the earlier logs was a red herring. `milestoneAttacher.run()`
asserts `GetBaseline() != nil` right after a successful `solidifyBaseline()`, so
`N/A` only means the failure happened *inside* baseline solidification — where
the `Bad` case does `a.setError(baselineDirection.GetError())`, inheriting the
ancestor's error verbatim. Most of the log lines were therefore inherited text,
not the logged transaction's own verdict. That is why the identical
`conflicting branch endorsement` string reappeared on transactions hundreds of
slots downstream, and why the printed baseline sometimes equalled the
"conflicting" branch.

The decisive trace:

```
solidify s24481-14: direction s24481-0-018115d57222.. (isBranch=true), floor s24480-0-017b7fc7a9d3..
solidify s24481-14: direction s24481-0-018115d57222.. UNDEFINED, pulling
endorsement CONFLICT: endorsed s24481-0-018115d57222.. != baseline s24480-0-017b7fc7a9d3..
```

The baseline *direction* is correct — the same-slot branch the transaction
endorses. It is `UNDEFINED` and being pulled, so nothing was resolved. Yet the
attacher proceeded on the **floor**, the previous slot's branch.

**Mechanism.** The floor and the resolved baseline are the same field.
`AttachTxID(WithBaselineFloor)` writes the floor onto the vid, and
`solidifyBaseline` reads that field to decide whether solidification succeeded:

```go
if ok := a.solidifyBaselineUnwrapped(v, a.vid); !ok { return vertex.Bad }
if bl := a.vid.GetBaselineBranchIDNoLock(); bl != nil { a.setBaseline(bl); return vertex.Good }
return vertex.Undefined
```

In the `Undefined` case `solidifyBaselineUnwrapped` returns via `pullIfNeeded`
without setting a baseline. The pre-set floor makes the field non-nil, so
`solidifyBaseline` mistakes it for a resolved baseline and returns `Good`. The
transaction is then rejected for endorsing its own same-slot branch, and `Bad`
propagates verbatim through its whole forward cone.

**Where the bad floor comes from.** Floor adoption is sound only while the floor
is a SUPERSET of the dependency's own baseline — true in normal operation, since
an attacher's baseline is its own slot's branch and its dependencies live in that
slot or earlier. It **inverts for a bootstrap transaction**, whose explicit
baseline is deliberately a past-slot branch (the LRB) while the dependencies
reached from it live in later slots. Confirmed on the wire: the tips carrying the
poisoning floor included `s24483-3` — tick 3, the bootstrap signature.

This is also why gap length was irrelevant and why a *degraded* network head was
the trigger: only there does the endorsed same-slot branch stay `Undefined` long
enough for the floor to win the race.

## Fixes

| commit | change |
|--------|--------|
| `d73b4142` | don't record a baseline floor older than the dependency (`txid.Slot() <= options.baseline.Slot()`) |
| `0b32150f` | issue a bootstrap transaction every slot while stuck, not every second slot |
| `53ce5315` | dagviz draws bootstrap transactions red |

`0b32150f` splits two conditions the old test conflated. Bootstrap state is now
read from the LRB (no branch for `bootstrapLRBLagSlots` = 3, about half a minute),
while the own-milestone check is kept purely as a one-per-slot limit. Previously
the own-milestone staleness proxy suppressed the next bootstrap for a slot, which
halved the rate at which others could consolidate coverage and, with sequencers
alternating out of phase, shrank the overlapping bootstrap surface per slot.

## Round 1: the standard 3-stop drawdown, unassisted attacher

Same down-leg (oloc1 -> oseq1 -> oloc2, halt at 49.25%), same unresolved multi-way
head fork that wedged all three nodes the day before. Result:

- last branch `s25196`, halt across slots 25197-25209 (~13 slots)
- `boot` issued a bootstrap transaction **every slot** (25198...25209, consecutive)
  while refusing every branch — first live confirmation of `0b32150f`
- branches resumed `s25211` and then **every slot**, not the previous half rate
- `BAD=0`, `floor1=0`, `floor2=0`, `notself=0` on every node

The previous half-rate branching was a symptom of the wedge (the third sequencer's
coverage arriving only via alternating bootstrap top-ups), not of the cadence.

## Round 2: total shutdown, restart smallest-first

All five sequencers stopped — the network fully dead, last branch `s25791` — then
restarted one at a time from the smallest share, so the bootstrap chain returned
last and the network had to recover without it.

| step | live coverage | outcome |
|------|---------------|---------|
| all stopped | 0% | dead |
| +`oloc1` | 9.86% | bootstrap txs every slot, alone, baseline `s25791` |
| +`oseq1` | 29.56% | coverage **combined**: both report `30_040_148_289_026` |
| +`oloc2` | 49.26% | all three on `50_062_049_037_986`, still short |
| +`hloc0` | **68.96%** | `endorse: 3` -> `SUBMIT BRANCH s25833` — network alive |
| +`boot` | 98.51% | full |

A ~42-slot branchless gap closed from cold, `BAD=0` fleet-wide throughout.

Two things this established that no earlier run did:

- **Coverage combines across independently cold-started sequencers.** Each new
  node's share folded into a single figure that all of them agreed on — observed
  directly, not inferred. This is the mechanism the entire restart path rests on.
- **The bootstrap chain is not required.** `boot` was down until after branching
  resumed, consistent with the submit gate having no bootstrap exemption: the
  carve-out in the build and attach paths really is inert for a live sequencer.

## The remaining gap: the sync gate

Every sequencer in both rounds started via `active (bootstrap)` — that is,
`do_not_wait_for_sync_at_start` was set on all of them. **These runs demonstrate
recovery *given* that flag.**

Without it, `IsSynced()` requires a healthy branch within one slot of now:

```go
return slotNow == 0 || multistate.FirstHealthySlotIsNotBefore(w.StateStore(), slotNow-1)
```

so every node is unsynced the moment branching stops, and `ensureSyncedIfNecessary`
will not start a sequencer during any halt. The automatic escape hatch,
`BootstrapFromOldState`, only fires when the committed state is more than
ST = half the branch-txID retention behind real time — 8740 slots, roughly 25
hours — so it never applies to an outage of this scale.

Net: on current code an unflagged network cannot restart itself from a full stop,
and a node restarted into a halt cannot rejoin. That is a design question rather
than a defect — the flag is the documented mechanism ("a bootstrap context that
must be active regardless of sync so a stalled network can be restarted by many
sequencers combining coverage") — but it means unattended recovery depends on
operators having set it in advance.

## Still open

- The floor / resolved-baseline **field conflation** in `vid.baselineBranchID`.
  `d73b4142` removes the bootstrap source of a bad floor, not the overloading.
  Forward sync also pins older baselines, so the same shape could recur there.
- `PastCone.SetBaseline` writes `delta.baselineBranchID` while `GetBaseline`
  returns the outer field when non-nil, so a baseline swapped by `MergePastCone`
  inside an IncrementalAttacher delta is invisible until `CommitDelta`.
  `baselineKnowsTx` reads the outer field directly. Real, latent, and NOT the
  cause of this wedge — the milestone attacher never opens a delta.
- Same-slot branch ties are resolved by `util.IndexOfMaximum`, which returns the
  first maximum and so depends on iteration order. Worth auditing for cross-node
  determinism.
- `synced: true` is reported while far behind a halted network; not a usable
  liveness signal.
