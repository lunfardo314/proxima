# Nothing-at-stake / Equivocation finding (2026-04-20 testnet run)

*Evidence and diagnosis from a live-testnet session. Stopped mid-investigation;
pick up from §6 in the next session. Binaries on all four machines at the time
of capture: `vcs.revision = 932add40` (tip of `develop07-pastcone-diag`).*

## 1. Summary

On the 2026-04-20 testnet run, `loc0` produced two signed, valid, competing
successor transactions to the same chain output `[320753|80sq]003cd1012296..[0]`:

- **successor #1**: `[320753|86sq]000803be6894..`, submitted at 10:48:21.129
  (chain-continuity extension inside slot 320753).
- **successor #2**: `[320754|79sq]0086a8b3dfae..`, submitted at 10:48:30.810
  (after `loc0`'s own branch got orphaned, resuming from a rolled-back tip).

This is **equivocation** — the classic nothing-at-stake pattern at the
sequencer chain-ID layer. The node did NOT run two instances with the same
key; a single process (`proxima[4121571]`) signed both successors after an
own-branch orphan event caused `loc0` to roll back its view of its own chain
tip. The protocol did not penalise it; the symptom at every other node is the
persistent `BAD(conflict …)` cascade we have been chasing for days.

## 2. Timeline

Wall-clock times UTC+2 (boot machine). `loc0` host is `63.250.56.190`.

```
10:46:40   loc0 STARTED (pid 4121571, after a ~40 min outage since 10:06:34).
10:48:12   loc0 SUBMIT BRANCH [320753|0br]01579bfdbecc  inflation 11_758_128
10:48:17   loc0 SUBMIT SEQ TX [320753|12sq]00d3dab7908f (endorse:0)
10:48:20   loc0 SUBMIT SEQ TX [320753|80sq]003cd1012296 (endorse:1) ← successor target
10:48:21   loc0 SUBMIT SEQ TX [320753|86sq]000803be6894 (endorse:1) ← successor #1,
                                                        consumes [320753|80sq][0]
10:48:23.048  TWO COMPETING BRANCHES submitted simultaneously:
             boot: [320754|0br]017828b5dfc1  branch bonus 53_141_664  (won coverage)
             loc0: [320754|0br]01f576b83261  branch bonus 26_286_372  (orphaned)
             boot's branch did NOT include [320753|86sq] in its past cone
             (gossip lag: submitted only 1.9 s before the branch race).

10:48:27   loc0 SUBMIT SEQ TX [320754|12sq]005bb9412b71  extends loc0's own,
                                                         now-orphaned branch
10:48:30   loc0 SUBMIT SEQ TX [320754|72sq]00bbb37e9d16
10:48:30.810 loc0 SUBMIT SEQ TX [320754|79sq]0086a8b3dfae ← successor #2,
                                                        ALSO consumes [320753|80sq][0]
10:48:31   loc0 SUBMIT SEQ TX [320754|88sq]0069cb635919
```

## 3. Diagnostic evidence (loc1, attaching a downstream tx)

The `past_cone_diag` trace tag was enabled. `loc1` rejects every incoming tx
whose chain passes through `[320754|79sq]`:

```
ATTACH [320755|79sq]005dbd2364db.. (baseline [320755|0br]019dda9fd6db..)
  -> BAD(conflict [320753|80sq]003cd1012296..[0] in the past cone)

TRACE(past_cone_diag) CONFLICT ok-flag:
  pc=[320755|79sq]005dbd2364db..
  baseline=[320755|0br]019dda9fd6db..
  vid=[320753|80sq]003cd1012296..
  output=0
  branchKnowsTx=TRUE
  pcConsumer=[320754|79sq]0086a8b3dfae..
  hasUTXO=false (state holds a different consumer or pc consumer was GC-stranded)
```

So the `S+` flag on `[320753|80sq]` is genuine (not stale) and the state
really has `[320753|80sq][0]` consumed — by successor #1, the one the state
committed. The past cone is trying to extend through successor #2, which the
state never committed. Neither the flag machinery nor the conflict checker is
buggy here; they correctly surface a real equivocation.

Relevant past-cone extract:

```
#0 S+ [319834|83sq]00edd2da0d8e..
#1 S+ [320752|0br]01727d63d469..
#2 S+ [320753|74sq]0040dbe56bb4..
#3 S+ [320753|80sq]003cd1012296..  consumers: {0: {[320754|79sq]0086a8b3dfae..}}
#4 S+ [320754|0br]017828b5dfc1..   consumers: {0: [320754|12sq]00df1132e927,
                                                1: [320755|0br]019dda9fd6db}
#5..S- [320754|12sq]00df1132e927..
#6 S- [320754|77sq]00119c699b8a..
#7 S- [320754|79sq]0086a8b3dfae..  consumers: {0: [320754|88sq]0069cb635919}
#8 S- [320754|88sq]0069cb635919..  consumers: {0: [320755|40sq]0020b9ca0f09}
#9 S+ [320755|0br]019dda9fd6db..
#10..12 S- follow-on seq txs
#13 S- [320755|79sq]005dbd2364db.. (tip)
```

Chain A (loc0's memDAG chain, rejected):
  `[320753|80sq]` → `[320754|79sq]` → `[320754|88sq]` → `[320755|40sq]` → tip

Chain B (state-committed loc0 chain, invisible to the past cone):
  `[320753|80sq]` → `[320753|86sq]` → [ … boot's [320754|0br] branch
  committed whatever loc0 tip was visible then ] → …

Both are cryptographically signed by loc0's chain key.

## 4. Root cause reconstruction

1. loc0 is a sequencer that started at 10:46:40 after a ~40 min outage. It
   rebuilt peer graph and caught up via forward_sync.
2. At slot 320753 it produced `[320753|80sq]` and then `[320753|86sq]`, the
   second consuming the first. Fire-and-forget; gossip propagation begins.
3. At the slot 320753 edge both `boot` and `loc0` generated branches for slot
   320754 at the same wall-clock instant. Boot had not yet received loc0's
   `[320753|86sq]` — it had only seen up to `[320753|80sq]`. Boot's branch
   coverage count included `[320753|80sq]` but not `[320753|86sq]`.
4. Boot's branch had higher coverage (inflation bonus 53M vs. 26M — boot has
   higher effective stake) and won. loc0's branch was orphaned; the whole
   subtree hanging off loc0's branch became orphan in loc0's memDAG.
5. loc0's sequencer observed the orphan (or lost sight of its subtree via
   memDAG GC / past-cone merge rejection) and resumed producing by consulting
   its own latest milestone. That path is `OwnLatestMilestoneOutput()`. With
   the orphan cleared, the tippool no longer had loc0's orphaned milestones;
   the fallback `bootstrapOwnMilestoneOutput()` walked the committed state
   and found `[320753|80sq]` — NOT `[320753|86sq]`, because boot's branch
   committed up to `[320753|80sq]` for loc0's chain and not beyond.
6. loc0 resumed by extending `[320753|80sq][0]` at slot 320754 tick 79 →
   `[320754|79sq]0086a8b3dfae`. **Equivocation.** loc0's key has now signed
   two valid extensions of the same output.
7. Every subsequent attempt by any node to attach a chain that transits
   `[320754|79sq]` fails with a real `BAD(conflict …)` because the state's
   committed consumer of `[320753|80sq][0]` is `[320753|86sq]` (via boot's
   branch mutations), not `[320754|79sq]`.
8. Cascade: as long as loc0 keeps producing on the rejected fork, every
   downstream sequencer that endorses or includes loc0's chain imports the
   bad subtree and gets stuck.

## 5. What is / isn't correct in the code

**Protocol-side (correct as is):**
- Past-cone `S+` flag is set from the genuine `BranchKnowsTransaction` result
  against the current baseline.
- `_checkVertex` correctly detects `inTheState && !HasUTXO` as a conflict,
  and the diagnostic confirms both conditions were accurate.
- The diagnostic output locates the root — it showed this is a real fork,
  not a past-cone machinery bug.

**Sequencer-side (buggy — the equivocation origin):**
- `OwnLatestMilestoneOutput()` + `bootstrapOwnMilestoneOutput()` have no
  memory of what this sequencer's key has already signed. When a
  just-signed-but-now-orphaned subtree is discarded, the sequencer has no
  record to prevent signing an extension of an output whose other signed
  extension was orphaned.
- The operator has no way to know this happened; the node logs show nothing
  abnormal (each submit succeeds from loc0's perspective).

**Network-wide (consequential design gap):**
- Nothing-at-stake mitigation is entirely social / coverage-driven. A forked
  sequencer continues producing on its side fork without penalty; other
  honest nodes silently reject its chain as `BAD` but cannot quarantine the
  offender. The only currently-visible symptom is the conflict cascade in
  other nodes' logs.

## 6. For next session (pick up here)

The user stopped the testnet after this finding. Investigation plan for the
next session:

1. **Reproduce at the DB level.**
   - On loc0: `proxi db tx [320753|86sq]000803be6894` and inspect its past
     cone / inputs.
   - On loc0: `proxi db tx [320754|79sq]0086a8b3dfae` and confirm its actual
     input is `[320753|80sq][0]` (not `[320754|72sq][0]` as chain-continuity
     would predict).
   - On loc0: `proxi db branch [320754|0br]017828b5dfc1` — inspect state
     mutations; confirm whether loc0's chain entry is at `[320753|80sq]` or
     `[320753|86sq]`.
   - On boot: `proxi db branch [320754|0br]017828b5dfc1` — past cone of
     boot's winning branch, confirm whether `[320753|86sq]` is included.

2. **Trace `OwnLatestMilestoneOutput` and `bootstrapOwnMilestoneOutput`.**
   - Identify exactly where loc0 decided to extend `[320753|80sq]` at slot
     320754 tick 79 instead of its own later `[320753|86sq]`.
   - Read `sequencer/own_milestones.go:84` (`OwnLatestMilestoneOutput`) and
     the `bootstrapOwnMilestoneOutput` fallback. Understand when and how
     the bootstrap walk is consulted after an orphan.

3. **Design the own-chain-equivocation guard.**
   The invariant we want: *a sequencer never signs an extension of an output
   for which its key has already signed another successor*. Options:
   - Keep an own-signed-tips registry in the sequencer (persistent across
     orphan events, cleared only when the signed tip appears in a committed
     descendant branch's state — i.e., it's settled). Before building a new
     milestone, check: "have I signed something consuming this input?" Refuse
     if so, and extend from the already-signed tip instead.
   - Make `OwnLatestMilestoneOutput` return the maximum of
     (tippool latest, own-signed-tips registry tip, state-committed tip) —
     never lower than any of them.
   - Decide what to do when an own-signed tip was orphaned AND cannot be
     re-included (e.g., its branch was rejected network-wide): either
     retry with explicit baseline to a newer branch via boot proposer, or
     give up and re-bootstrap at the cost of one orphaned suffix but without
     equivocation.

4. **Network-level mitigation (optional, later).**
   Consider whether to detect and quarantine equivocating chain IDs at the
   peer level: a node that sees two conflicting own-signatures from a single
   ChainID could mark that ChainID as "bad until next LRB refresh" and stop
   pulling/endorsing its milestones. Would need a deterministic rule so
   honest nodes converge.

5. **Regression test.**
   Reproducible scenario: two sequencers race branches for the same slot,
   one's branch loses, the losing sequencer has produced a seq tx just
   before the boundary that the winning branch did NOT include. Confirm that
   with the guard in (3), the losing sequencer extends from its own signed
   tip rather than the committed state's view.

## 7. Code references (as of `932add40`)

- Conflict detection: `core/vertex/past_cone.go:1039` `_checkVertex`
- Diagnostic cross-check: `core/vertex/past_cone.go:~430` `diagLogSuspectConflict`
- Sequencer own-tip lookup: `sequencer/own_milestones.go:84` `OwnLatestMilestoneOutput`
- Bootstrap fallback: `sequencer/own_milestones.go` `bootstrapOwnMilestoneOutput`
- Submit path: `sequencer/strategy_async.go:~305` `submitMilestone` +
  `recordPendingSubmit`. This is where a signed-tips registry would be
  populated.
- Throttle / pacing (already in place, unrelated to the equivocation itself):
  `sequencer/strategy_async.go:~41` `isOverloaded` + `strategy.go`
  `onMilestoneConfirmed` clearing.
