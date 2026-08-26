# Resilience: spam and DDoS protection, survivability, recovery

How a Proxima node and the network as a whole stay alive under hostile or merely
excessive load: what the design assumes an attacker can do, the layers that
answer each of those, the order in which a node gives things up as pressure
rises, and how it gets back to normal afterwards.

The second half is a reference inventory of every gate — everything that can
reject, defer, drop, rate-limit or slow a transaction — with what tunes it and
which layer owns it. It is also the place to start when a node is shedding load
and you need to know **which** gate is doing it: see
[Which gate is dropping my transactions](#which-gate-is-dropping-my-transactions).

Every value here was verified against `develop`. Fixed size and count ceilings
that do not depend on load are in [`ledger/limits.md`](../ledger/limits.md).

---

# Part I — Architecture

## 1. What the design has to survive

Proxima's attack surface is unusual, and the differences are what shape the
defence.

**There is no global mempool and no block proposer.** Nothing accumulates
pending work waiting for someone to select from it, so the classic
"fill the mempool" attack has nothing to fill. A sequencer node keeps a backlog
of transactions that tag along to *its* sequencer, and that is the closest thing
to a mempool in the system — bounded, local, and useless to an attacker who is
not paying that sequencer.

**There is no leader and no committee.** There is no rotating proposer to
predict and target, and no quorum whose availability the network depends on.
Taking down any one sequencer removes one participant, not a turn in a schedule.

**Every transaction is a write to the shared graph**, so the ability to write is
exactly the thing that must be rationed. That rationing is economic: it lives on
the ledger, not in the node.

Against that, four adversary goals and one non-adversarial one:

| Goal | Shape of the attack | Answered by |
|------|--------------------|-------------|
| **Spam** | Many valid, cheap, useless transactions | Ledger cost of writing (§2.1), per-sender pace (§2.2) |
| **Asymmetric work** | One transaction that makes a node do unbounded work | Bounded work per transaction (§2.3) |
| **Resource exhaustion** | Drive memory or disk to the limit | Shed rather than queue (§2.4), TTLs and the memDAG backstop, memory watchdog |
| **Liveness / isolation** | Prevent a node from catching up, or split it onto a fork | Recovery loops (§5), fork re-anchoring, branch health |
| **Honest overload** | A legitimate load spike, a restart under load, a slow disk | Graceful degradation (§4) |

## 2. The five principles

### 2.1 The cost of writing is on the ledger, not on the node

The first line of defence is not a rate limiter, it is economics. To emit a
transaction an attacker needs tokens, and three ledger rules turn that into a
real cost:

- **The dust rule.** Every produced output must carry a minimum storage deposit,
  computed from its *effective* size — the UTXO's own bytes plus the trie rows
  its index-values entries will occupy. A plain sigLock output is 49 bytes,
  effective size 117, and must hold **9,250,000 motes**. Spam that creates
  outputs therefore locks up capital per output, permanently, for as long as the
  output exists.
- **Sender must be known.** The first transaction from a holder ID that owns
  nothing in the latest reliable branch is refused outright. A fresh identity is
  not free; it must first be funded by someone who is already known.
  **Mining transactions are exempt** — a miner's holder ID is by construction
  not on the ledger yet — and pay with proof of work instead, enforced by the
  `mineLock` covenant. Branch transactions are exempt because the check is made
  against a possibly stale local view (§7).
- **Transaction pace.** A holder may not issue transactions closer together than
  the ledger pace — 12 ticks normally, 3 for sequencer transactions. Buying more
  throughput means buying more funded identities, each of which had to be funded
  by an existing one.

Together these mean the flood rate an attacker can sustain is bounded by capital
committed on the ledger — or, on the mining path alone, by hashrate — and every
node computes that bound identically because it is a ledger rule. This is the same Sybil-resistance argument the consensus
itself rests on, reused for spam.

### 2.2 Identify the sender before spending work

Every Proxima transaction carries **exactly one signature**. That is a
deliberate design choice, and spam prevention is one of its reasons: one
signature means one unambiguous **holder ID**, so per-sender accounting is
possible at the very edge of the node, before any expensive work.

The reception path is ordered cheapest-first so that the work an attacker can
force per message stays small:

```
dedup (map lookup)  →  parse (structure)  →  prefix check  →
sender pace (ring buffer)  →  time bounds  →  signature (stage 2)
```

Signature verification — the expensive step — happens *after* the pace check has
already thrown out a flooding sender. Note the consequence: the pace check reads
the holder ID out of the transaction without having verified the signature yet.
That is sound for rate-limiting because a forged holder ID only rate-limits the
identity it names, and the transaction is discarded at stage 2 regardless.

### 2.3 Bound the work one transaction can cause

A transaction does not only cost what it is; it costs what it *drags in*. Its
past cone must be solidified, which can mean pulling dependencies from peers,
which pull further dependencies. Left unbounded, one crafted transaction pointed
at an ancient part of the graph would make a node materialize the entire history.

Three bounds close that:

- **Attachment cost budget** (550, a ledger constant): the total cost of the
  directly-reachable past cone plus the transaction's own inputs and outputs. It
  is set just above the cost of a maximal transaction (256 inputs, 256 outputs),
  so honest traffic never meets it. Crossing it makes the transaction **Bad** —
  invalid, not merely deferred.
- **Recursive pull depth cap** (50 branches with forward sync, 500 without): how
  far back the solidification walk may go. It applies to branch dependencies
  only, and applies *even when the branch is available locally*, because
  otherwise one far-ahead milestone makes the walk rebuild the whole branch chain
  back to genesis out of the local txstore. This was the 2026-06-14 lagging-node
  wedge.
- **Pull patience** (30 attempts, 2 s apart): a dependency that never arrives
  eventually fails the attacher instead of pinning it forever.

The depth cap is deliberately a **pure constant** given the configuration. The
attacher knows nothing about forward sync, the LRB or any frontier — depth is
the only thing bounding the backward pull, and the only base case that
terminates the recursion is "dependency already in committed state". Coupling
this to a moving frontier caused the 2026-06-20 freeze.

### 2.4 Shed, don't queue — and stay a good citizen while shedding

This is the load-shedding property the whole design leans on, and the ordering
in the reception path is what makes it safe.

By the time a node decides whether to attach an unsolicited transaction, it has
**already persisted it to the txstore and already gossiped it to peers**.
Dropping it therefore:

- **loses no data** — the bytes are on disk, and any past cone that later turns
  out to need the transaction can pull it back;
- **does not harm the network** — the node has already relayed it, so its
  neighbours see it whether or not this node had capacity to attach it.

An overloaded Proxima node is still a useful relay and a useful archive. That is
why the node can afford to drop aggressively, and why dropping is the *first*
tool it reaches for rather than the last.

The one exception is the class of transactions that cannot be pulled back:
this node's **own sequencer milestones** (nobody else will offer them) and, on a
sequencer node, the tag-alongs **targeting its own sequencer** (that set is its
mempool). Both are exempt from shedding.

### 2.5 Degrade in a defined order, and fail loudly

Every remaining pressure valve is arranged so that the node gives up the least
valuable thing first and the network's integrity last (§4). When nothing is left
to give up, the node **shuts down gracefully** rather than wedging silently or
being OOM-killed — twice: at 100% memory stress, and when an attacher hits the
depth cap on a node with no sync sources configured. A wedged node that looks
alive is worse than one that exits, because the operator does not learn about it.

## 3. Where the defences sit

```
peer gossip ─┐
             ├─►  ingress  ─►  txinput_queue  ─►  attach gate  ─►  clock
API ─────────┘     §6            §7                 §8              §9
                 size, peer      dedup, pace,      shed under      defer until
                 identity,       signature         pressure        ledger time
                 protocol id                          │
                                                      ▼
   committed state ◄── commit ◄── attacher ◄── memDAG
                                   §11          §12
                                  budget,      TTLs, size
                                  depth,       backstop
                                  conflicts,
                                  stage 3

   solicited (pulled) transactions enter at the attacher directly — §10
```

Issuance — what this node's own sequencer decides to *put on* the network — is a
separate set of gates (§13), and it is where most system-wide throttling comes
from: a sequencer that throttles itself reduces the load every other node has to
absorb.

## 4. Graceful degradation: the order things are given up

As pressure rises a node sheds in roughly this order. Each step costs something,
and the cost is stated so the sequence is auditable.

| # | Under pressure | Node gives up | Cost |
|---|----------------|---------------|------|
| 1 | Sequencer milestone builds fail or run late | **Tag-along capacity.** The AIMD controller cuts the tag-along budget from 2/3 of the cost budget to 1/3 to zero | Other people's transactions wait; the sequencer keeps its own chain moving |
| 2 | Attacher count at the cap | **Fresh sequencer gossip.** Newer sequencer transactions are dropped; older ones still pass | Some milestones are attached late or pulled later |
| 3 | Node is catching up | **Everything except branches** | Backlog and tippool go stale; catch-up is prioritised |
| 4 | Self-attachment latency > ~1 s, or coverage delta < 7/12 | **Branch production.** The sequencer declines to submit a branch | This node stops committing state; others still can |
| 5 | Snapshot in progress | **All unsolicited transactions** | A bounded window of relay-only behaviour |
| 6 | memDAG over 50,000 vertices | **Old vertices, forcibly.** Dependency edges severed past the TTL, ignoring the active-attacher guard | Some attachers fail and their transactions must be re-attached later |
| 7 | Memory stress ≥ 80% | Nothing yet — warnings only | — |
| 8 | Memory stress = 100% | **The node.** Graceful shutdown | Downtime; recovery per §5 |

Steps 1–3 are invisible to the network. Step 4 is the first one that shows up in
consensus, and it is deliberately placed after everything cheaper: refusing to
build a branch is how a node avoids committing state it is not confident in.

## 5. Survivability and recovery

### 5.1 What recovers by itself

| Situation | Mechanism | Trigger |
|-----------|-----------|---------|
| Node falls behind | **Sync mode.** An attacher's backward walk hits the depth cap, registers that branch as a sync target, and the node enters sync mode | Automatic; `SyncTargetsPending()` |
| Node is behind by more than recursion can bridge | **Forward sync.** Pulls branches from configured `sources`, ascending, 5 in parallel, committing up to 10 per tick, until the targets are met and the set empties | Automatic when `sources` are configured |
| Node is on a fork | **Re-anchor.** Fork detection probes the source's canonical lineage in 100-slot windows, finds the common ancestor and re-roots onto the canonical lineage; the sequencer is held off until re-rooted | Automatic |
| Memory spike | Forced GC above 50% of the limit; async GC worker pinged above 60% | Automatic, once `memory.limit_mb` is set |
| memDAG leak | Size backstop force-detaches past-TTL vertices | Automatic |
| Missing or corrupt database at startup | **Snapshot restore.** `CheckAndRestoreOnStartup` finds the newest snapshot in `snapshot.directory` and restores from it | Automatic at startup |
| Transaction dropped under load | Pulled back on demand when some past cone needs it | Automatic |
| **The whole network has stopped branching** | **Bootstrap transactions** with an explicit baseline, until enough sequencers meet in one slot for coverage to consolidate past the health threshold (§5.5) | Automatic, uncoordinated |

### 5.2 What needs an operator

- **Behind by more than `sync.max_slots_behind` (8,740 slots).** Forward sync
  refuses rather than attempting a very heavy forward build; restore from a
  fresher snapshot instead.
- **Depth cap reached with no `sources` configured.** The node shuts down
  gracefully and says so: recursion alone cannot reach committed state.
- **Memory watchdog shutdown.** The node exits; restarting it is the operator's
  call, and §5.3 applies.
- **Frozen coverage expired while the network was down.** A restarting sequencer
  can be permanently unable to build a healthy branch. This is what the two
  suppression switches exist for (§5.4).

### 5.3 Restart is a hazard, not a remedy

Restarting a sequencer under live load can fork committed state — it has
happened, and it produced an endorsement storm. The safe sequence is to let the
node drain, or to accept downtime, rather than to cycle a loaded sequencer
repeatedly. Forward sync will re-anchor a forked node (§5.1), but re-anchoring
is repair, not something to invoke on purpose.

### 5.4 Deadlock-avoidance carve-outs

Several rules are deliberately *not* enforced on the ledger, or are locally
suppressible, because strict enforcement can lock a node out permanently. They
are worth knowing as a group, because each is a place where correctness was
traded against liveness on purpose:

| Carve-out | Why |
|-----------|-----|
| **Branch health is a convention, not a ledger rule** — and relaxable over a bounded slot window (`health_relief`) | A network whose frozen coverage expired while it was down could otherwise never rebuild a healthy branch. Relaxing it is a decision the *whole network* takes together: every node must run the same window and fraction, or they disagree about which branches are reliable. A relief fraction below 1/2 would let a minority advance consensus alone, which is precisely what the threshold prevents |
| **Coverage lower bound enforced in Go, suppressible** (`suppress_coverage_contribution_lower_bound`) | Same class: a small-balance sequencer restarting after frozen-coverage expiry could be permanently stuck below the bound. The *upper* bound stays a ledger constraint — no deadlock risk there |
| **Own sequencer milestones exempt from the attach gate** | They cannot be pulled back if dropped; the node would starve its own chain |
| **Mining transactions exempt from the unknown-sender rule** | A fresh miner's holder ID is by definition not on the ledger yet; the `mineLock` structure and its proof of work gate that path instead |
| **Branch transactions exempt from the pace check** | The final pre-branch consolidation milestone may legitimately land one tick before the branch; mirrors the ledger's own `scanInputs` exemption |
| **Branch transactions exempt from the unknown-sender rule** | The rule is evaluated against this node's own LRB, which may be stale. Refusing a branch because a lagging view does not recognise its sequencer would block catch-up — the branch is what would fix the staleness (§7) |
| **Bootstrap sequencer exempt** from the coverage lower bound, and `ForceActivity` keeps its tag-along budget off zero | Something has to keep producing for the network to have any liveness at all |

### 5.5 Uncoordinated restart: bootstrap transactions

The hardest recovery case is not one node failing but the **whole network
stopping**. If branches stop being produced for long enough, every sequencer's
chain output ends up far in the past — and then there is nothing recent to
endorse. That is a deadlock: coverage needs endorsements, endorsements need
recent transactions, and recent transactions need someone to go first.

**Bootstrap transactions** break it. A bootstrap transaction is a non-branch
sequencer transaction with an **explicit baseline** — it names the latest
reliable branch directly instead of reaching it through endorsements — so it can
be built when there is nothing to endorse at all.

The recovery is *uncoordinated*. Nobody announces a restart time and nobody
agrees on one. Each sequencer independently notices the same thing from its own
view of the ledger and starts emitting:

| Element | Value | Why |
|---------|-------|-----|
| Stall condition | LRB more than 3 slots behind the target slot | Normal operation keeps the LRB within a slot or two, so this only trips once branches have actually stopped. It also keeps the explicit baseline in a past slot, which the ledger requires |
| Rate | At most one bootstrap transaction per sequencer per slot | It is a re-anchor, not a stream |
| Timing | Only in the first quarter of the slot | The bootstrap transaction is what *other* sequencers consolidate their coverage on, so it must leave them most of the slot to do it |
| What it extends | The sequencer's chain output **as committed in the baseline** — not its own latest milestone | Extending the milestone would chain each bootstrap transaction onto the last, growing an uncommitted cone that drifts further from the state it is meant to re-anchor to. Re-anchoring to the same committed output every slot makes them siblings instead, and the ones nobody consolidates are simply orphaned |

Then the convergence. Each bootstrap transaction on its own carries very little
coverage. But once several sequencers have issued one **in the same slot**, they
can endorse each other — there is now something recent to endorse. Coverage
consolidates across them and grows slot by slot, and as soon as it crosses the
7/12 health threshold branches become possible again and the network takes off
under its own rules, with healthy and secure coverage behind every branch.

Nothing in that sequence is negotiated. It is the ordinary
biggest-coverage-wins behaviour, restarted from a floor that the explicit
baseline makes reachable.

Two details worth knowing. A bootstrap transaction has no endorsements and an
explicit baseline, so its past cone is nearly empty and almost the whole
attachment cost budget is free — which makes it the cheapest place in the slot
to re-freeze delegations that unfroze while the network was down, and that is
what the bootstrap proposal spends its budget on. And because the event is rare
and significant, submission is logged at warning level (`SUBMIT BOOTSTRAP TX`),
where default verbosity cannot hide it.

The health relief window (§5.4) is the fallback for the case this cannot reach:
a network whose frozen coverage expired while it was down may be unable to
assemble 7/12 at all, and then the threshold itself has to be lowered — by every
node, identically.

### 5.6 Why the network survives even when nodes do not

- **No leader means no single target.** Removing a sequencer removes one
  participant, not a turn in a schedule.
- **A shedding node still relays and still archives** (§2.4), so load shedding
  does not fragment the network's view.
- **Gossip and pull are duals.** What gossip fails to deliver, an attacher asks
  for explicitly; what a node drops, a peer can supply again.
- **Branch health stops a weakened network from pretending.** Below 7/12 of
  supply behind it, honest nodes decline to build a branch rather than commit
  state a minority chose. The network stalls visibly instead of forking quietly.
- **And a stall is recoverable without coordination.** Bootstrap transactions
  (§5.5) let sequencers re-anchor to committed state with no one to endorse,
  meet in a slot, and consolidate coverage back above the threshold on their
  own.
- **Peers with different ledger versions cannot even connect** — the libp2p
  protocol name embeds the library hash — so an upgrade mismatch is a clean
  partition rather than a stream of mutual validation failures.

---

# Part II — Gate inventory

Each gate is tagged with the layer that owns it, because that determines what
changing it costs:

| Layer | Changing it means |
|-------|-------------------|
| **ledger** | A ledger constant or EasyFL constraint. A hardfork — every node must agree, or they disagree about validity |
| **config** | A node config key. Local to the operator, no coordination |
| **const** | A hardcoded Go constant. Needs a rebuild; the same value everywhere is assumed, not enforced |
| **convention** | Not a validity rule, but a rule honest nodes follow so as not to fork. Relaxing it locally is safe only if everyone relaxes it identically |

## 6. Ingress

| Gate | What it measures | On trip | Tuned by | Layer |
|------|------------------|---------|----------|-------|
| P2P frame cap | Declared frame size | Stream read fails, message discarded | `MaxPayloadSize` = 65,531 (`peering/misc.go`) | const |
| Ledger-version match | First 8 bytes of the library hash, embedded in the libp2p protocol name | Nodes never speak: no shared protocol ID | `peering/types.go` | ledger |
| Unknown incoming peer | Peer not in `PreConfiguredPeers` | Refused unless autopeering is on | `peering.max_dynamic_peers` | config |
| Dynamic peer count | Alive dynamic peers | No new peers added | `peering.max_dynamic_peers` | config |
| Pull requests refused | — | All incoming pull requests ignored | `IgnoreAllPullRequests` | config |
| Pull from dynamic peers | Peer static or not | Pull ignored | `AcceptPullRequestsFromStaticPeersOnly` | config |
| API request body | POST body on `/api/v1/submit_tx` | Body read fails → `stage="parse"` | `maxTxUploadSize` = 2 MiB | const |
| Stream connections | Concurrent websocket clients | Refused at capacity | `max_connections`: dagviz 5, mining stream 50 | config |
| Stream slow consumer | Per-connection send buffer | Message dropped and counted; ping/pong deadline closes the connection | `api/streaming/` | const |

The 2 MiB API cap is far larger than any valid transaction because the body also
carries `consumed_utxos`; the transaction is still bounded by
`MaxTransactionSize` (64 KB) at parse.

## 7. Reception — `txinput_queue`

The densest cluster in the system, in this order.

| # | Gate | What it measures | On trip | Layer |
|---|------|------------------|---------|-------|
| 1 | Dedup (`inGate`) | Transaction ID seen before, within TTL | Dropped; `proxima_txInputQueue_repeating` bumped | const |
| 2 | Stage-1 parse | Structure, sizes, counts | Warning, dropped | ledger |
| 3 | Prefix consistency | Gossip message prefix vs the real txid | Warning, dropped | const |
| 4 | Sender pace | Timestamp distance from this holder's recent transactions | Dropped, `rate_control` warning | ledger (pace) / config (on-off) |
| 5 | Unknown sender | Holder ID has no outputs in the LRB | Dropped — **except mining and branch transactions** | const |
| 6 | Time upper bound | Timestamp more than 6 slots in the future | From API or peer: txid marked invalid. Otherwise warning only | const |
| 7 | Stage-2 validation | Signature, partial context | Txid marked invalid | ledger |

**Dedup** is an exact map keyed by transaction ID with a 60-slot (~10 min) TTL,
purged when it exceeds 10,000 entries and rebuilt every minute to release map
memory. An entry marked *pulled* passes a second time — that is how a solicited
transaction gets in after its arrival was recorded.

It is an exact map rather than a bloom filter deliberately. Each entry carries
state (whether the transaction was pulled, which decides both the second pass
and whether it is gossiped), which a membership approximation cannot hold; and a
false positive would silently drop a transaction the node never saw, with no way
to notice, since unsolicited gossip is not retried. The TTL and purge threshold
keep the exact map to a few tens of thousands of entries, so it is affordable.

**Unknown sender** refuses the first transaction of a holder ID that owns
nothing in the latest reliable branch. Two exemptions, for different reasons.

*Mining transactions* bypass it because the miner's identity is new by
construction; the `mineLock` covenant's proof of work gates that path instead.

*Branch transactions* bypass it because the check is made against **this node's**
LRB, which may be stale. An unknown sender is therefore ambiguous: it may be an
attack, or the node may simply be lagging and not yet have seen the sender
funded. The two cases split on what a wrong guess costs. Dropping a non-branch
transaction is cheap — it can be pulled back later if some past cone needs it.
Dropping a branch is not: branches are how a node learns it is behind, how it
advances committed state and what drives sync, so refusing one because a stale
LRB does not recognise the sequencer would block the very thing that would fix
the staleness. The exemption resolves the ambiguity in favour of catching up.
Branches are also the least attractive thing to forge — one must carry a
healthy coverage delta to be worth anything.

**Sender pace** keeps the last 4 timestamps per holder in a ring buffer and
refuses a transaction closer than the pace to any of them. Branches are exempt;
solicited transactions skip it entirely; the check can be switched off per class
(`checkSeq`, `checkNonSeq`). The sender map is swept every 10 s against a
`360 × 127`-tick horizon and cloned every 5 minutes, so a burst of one-off
senders cannot pin memory.

## 8. The attach gate

Passing validation does not mean the node will attach. The transaction is
already persisted and already gossiped by this point (§2.4); this gate decides
only whether to spend memDAG and attacher resources now.

Solicited transactions and this node's own sequencer milestones skip it.

| Condition | Applies to | Counter |
|-----------|-----------|---------|
| Snapshot in progress | everything unsolicited | `tx_drop` |
| Sync targets pending, non-branch | sequencer milestones | `sync_drop` |
| Sync targets pending | non-sequencer | `sync_drop` |
| Attacher cap reached, transaction not older than the newest attached | sequencer transactions | `seq_drop` |
| No local sequencer | non-sequencer | `nonseq_drop` |
| Local sequencer, transaction does not target it | non-sequencer | `nonseq_drop` |

**Anti-starvation.** At the cap, a sequencer transaction is dropped only if its
timestamp is *not older* than the latest already attached, so a backlog drains
instead of being permanently outrun by fresher gossip.

**Far-ahead gossip is deliberately not shed.** It must be allowed to attach so
its past-cone recursion reaches the depth cap and flips the node into sync mode
— the shedding rule must not disable the mechanism that detects being behind.

**An access node drops all unsolicited non-sequencer transactions.** It issues
nothing and can pull anything it needs. A sequencer node keeps exactly those
targeting its own sequencer; the non-sequencer rate there is controlled by the
sequencer's attachment budget (§13), not by dropping.

## 9. Clock alignment

A transaction whose ledger time has not arrived is **deferred**, not rejected: a
goroutine sleeps until the timestamp is due, then attaches.
`proxima_general_gauge_wait` is how many are held this way. With the 6-slot
future bound (§7) this is bounded above.

## 10. The bypass lane

`txsolicit_queue` is the fast track for transactions the node asked for. It has
**no dedup, no rate control, no gossip and no attach gate** — everything pushed
in is attached.

That is sound only because the *demand* side is bounded: a pull happens because
some attacher needs a specific dependency, and the depth cap, cost budget and
pull patience (§11) bound how many that can be. The bypass lane is exactly as
safe as those three.

## 11. Attachment — `core/attacher`

| Gate | What it measures | On trip | Tuned by | Layer |
|------|------------------|---------|----------|-------|
| Attachment cost budget | Past-cone cost + own inputs+outputs | `ErrAttachmentBudgetExceeded`; milestone attacher marks it **Bad** | `constAttachmentCostBudget` = 550 | ledger |
| Concurrent attachers | Live attacher goroutines | New sequencer transactions shed at the attach gate | `workflow.max_concurrent_attachers`, default `max(20, 10 × GOMAXPROCS)` | config |
| Recursive pull depth | Branches walked backwards | Stop descending; register the branch as a forward-sync target | 50 with forward sync, 500 without | const |
| Pull patience | Pull attempts per dependency | `ErrSolidificationDeadline` | 30 attempts × 2 s | config |
| Build deadline | Wall clock inside one proposal build | Build aborts and returns what it has | proposal build budget | const |
| Vertex TTL self-abort | Age of the attacher's own vertex | Attacher aborts before the memDAG force-detaches its vertex | 24 slots | const |
| Conflict detection | Double spend anywhere in the past cone | Bad | — | ledger |
| Stage-3 validation | All UTXO constraints, full context | Bad | — | ledger |
| Sequencer coverage lower bound | Branch's own sequencer output: balance + frozen coverage | Branch rejected | `suppress_coverage_contribution_lower_bound` | ledger constant, Go enforcement, suppressible |

Both attacher kinds enforce the cost budget identically during descent; only the
reaction differs. The incremental (proposal-building) attacher may *lower* its
effective budget below the ledger cap to self-throttle — the mechanism §13
drives.

## 12. memDAG

Eviction rules, not rejections. Correctness must never depend on what the cache
happens to still hold.

| Rule | Value | Layer |
|------|-------|-------|
| GC pass period | 5 s, or on request | const |
| Wall-clock vertex TTL | 24 slots | const |
| Ledger-time vertex TTL | 12 slots behind the latest committed branch | const |
| Confirmed-deep pruning | 3 slots behind the LRB | const |
| Size backstop | 50,000 vertices | const |
| Branch tracking map | 20 records; cleared if the LRB is 24 slots stale | const |

The **size backstop** is a pure OOM safety valve and never trips in healthy
operation — steady state is a few thousand vertices. Over the cap, the GC
force-detaches every past-TTL vertex, severing input and endorsement edges
*regardless of the active-attacher guard*, so producers pinned by old vertices
become collectible. Consumer (`consumed`) forward edges are kept even then:
conflict detection, mutation generation and cone cleanup all depend on them.

The ledger-time TTL exists because forward sync produces vertices that are fresh
by wall clock and ancient by ledger time.

## 13. Issuance — the sequencer

**The tag-along budget is an AIMD congestion controller.** A `budgetLevel` in
0..6 starts at 6; each successful milestone adds 1, each failure (deadline
exceeded, no proposals) subtracts 3. The tag-along phase then builds against
`(2 × level / 6) / 3` of the ledger cost budget: a healthy sequencer uses 2/3 of
the cap, an overloaded one falls to 1/3 and then to zero tag-alongs. Slow ramp
up, sharp cut down — the same shape as TCP congestion control, for the same
reason. Delegations are inserted afterwards against the *full* budget, using
whatever the tag-along phase left.

`ForceActivity` raises the floor to 1/3 instead of zero; `DisableThrottle` pins
it at full.

| Gate | What it measures | On trip | Tuned by | Layer |
|------|------------------|---------|----------|-------|
| Tag-along budget | Milestone success/failure history | Tag-along phase gets a fraction of the cost budget | levels 0..6, +1/−3 | const |
| Max tag-along inputs | Tag-alongs per milestone | Stop inserting | `max_tag_along_inputs`, default 15 (0 = none) | config |
| Tag-along drain rate | Target tag-alongs per slot | Backlog drained no faster | `tag_along_drain_rate`, default 100 | config |
| Max frozen delegations | Delegations already frozen into a reachable epoch | Freeze refused; retried later | `max_frozen_delegations`, default 300 | config |
| Self-attachment latency | Time from submitting an own milestone to seeing it in the tippool | **Branch not submitted** | 12 ticks | const |
| Branch health | Coverage delta as a fraction of supply | **Branch not submitted** | 7/12, `constHealthyCoverage{Numerator,Denominator}` | ledger constant, convention |
| Network connectivity | Node connected at all | Branch not submitted | `sequencer.standalone` | config |
| Branch deferral | Ticks to wait for a peer branch of the same boundary | Branch delayed | `branch_deferral_ticks`, default 12 | config |
| Backlog blacklist | Output permanently unconsumable | Skipped for 5 minutes | `blacklistTTL` | const |
| Sequencer pace | Timestamp distance to the predecessor | Candidate not eligible | 3 ticks | ledger |

**Self-attachment latency** is the direct guard against the
submit-faster-than-attach spiral: if the sequencer's own last milestone has not
returned through its own pipeline within ~1 second, it stops issuing until it
does or the next slot's consolidation zone arrives.

## 14. Sync

| Gate | Value | Owner |
|------|-------|-------|
| Pull-ahead window | 5 branches pulled in parallel | `sync.pull_ahead` |
| Commit batch | 10 branches committed per tick | `sync.commit_batch` |
| Max slots behind | 8,740 — refuse, prefer a fresh snapshot | `sync.max_slots_behind` |
| Sync loop period | 1 s | const |
| Fork probe window | 100 slots, stepping back by 2 windows | const |
| Stall warning | 30 ticks quiet → ERROR, repeated every 60 | const |

`pull_ahead` and `commit_batch` bound per-tick throughput and pull concurrency
only. Neither bounds how far forward sync may ultimately reach — that is
governed entirely by the attacher handoff in §11.

## 15. Node-wide

| Gate | Threshold | Action | Layer |
|------|-----------|--------|-------|
| Memory watchdog | stress ≥ 80% | Warning every 5 s | const |
| Memory watchdog | stress = 100% | **Graceful shutdown** | const |
| GC ping | stress ≥ 60% | Async GC worker pinged, min 5 s between runs | const |
| Memory pressure GC | heap > 50% of limit | Forced GC | const |
| Soft memory limit | `memory.limit_mb` | `debug.SetMemoryLimit` | config |
| GOGC | `memory.gogc`, default 50 | `debug.SetGCPercent` | config |
| Snapshot in progress | — | All unsolicited transactions dropped | — |
| txstore write-behind | 100 transactions, 500 ms, 1,000-entry cache | Batched DB write | const |

Memory stress is recomputed every second as `100 × Alloc / limit`, and is 0 when
`memory.limit_mb` is unset — in which case **none of the memory gates exist**.
It is reported as `memory_stress_level` in the node info API.

## 16. Ledger constraints on the path

Validity rules, identical on every node, enforced at stage-3 validation.
Changing any of them is a hardfork.

| Rule | Value | Where |
|------|-------|-------|
| Minimum storage deposit (dust) | Piecewise-linear in effective output size; **9,250,000 motes** for a plain 49-byte sigLock output (effective size 117) | `ledger/transaction/validate.go`, schedule in `storageDeposit` |
| Storage-deposit exemptions | `stem`, `tagAlong`, `sendWithDeadline` | `ledger/sdeposit.go` |
| Produced outputs | 1–256 | parse |
| Endorsements | max 8 | `constMaxNumberOfEndorsements` |
| Duplicate inputs | rejected | EasyFL |
| Transaction pace | 12 ticks; 3 for sequencer transactions | `constTransactionPace*` |
| Attachment cost budget | 550 | `constAttachmentCostBudget` |

The effective size charged by the dust rule includes the trie rows an output's
index-values entries produce, not just the UTXO bytes — the deposit pays for
permanent state, not for message size.

## 17. What is not gated

Absence of a gate is as operationally relevant as its presence.

- **Serving pull requests is unrated.** `pull_tx_server` answers every request
  it can satisfy, one goroutine per response, with no per-peer and no global
  limit. The only controls are the peering-level ones in §6, which are
  all-or-nothing.
- **`/api/v1/eval` has no request-body cap.** It reads the body with
  `io.ReadAll` and no `MaxBytesReader`, unlike `/api/v1/submit_tx`. It evaluates
  only closed formulas, but the body itself is unbounded.
- **The solicit queue has no gates at all** (§10) — by design, bounded
  indirectly.
- **Three of the four drop counters never reach Prometheus** — see below.

## Which gate is dropping my transactions

Only `att`, `wait`, `call`, `store`, `prop`, `close`, `nonseq` and `nonseq_drop`
are registered as Prometheus gauges (`proxima_general_gauge_<name>`).
**`tx_drop`, `sync_drop` and `seq_drop` are internal counters only** — visible in
the periodic node stats log line (every 10 s), not in Grafana.

| Symptom | Look at | Gate |
|---------|---------|------|
| Transactions vanish, node behind | `sync_drop` in the stats line | Sync mode: only branches attach (§8) |
| Transactions vanish during a snapshot | `tx_drop` | Snapshot load shedding (§8, §15) |
| Sequencer gossip vanishes under load | `seq_drop`; `proxima_general_gauge_att` at the cap | Attacher cap (§8, §11) |
| Non-sequencer transactions vanish on an access node | `proxima_general_gauge_nonseq_drop` | Access node drops unsolicited non-seq (§8) |
| Sender reports rejections | `rate_control` warnings | Sender pace, or unknown sender (§7) |
| `proxima_general_gauge_wait` large | — | Clock alignment: transactions are early, not stuck (§9) |
| Sequencer stops taking tag-alongs | `budget: N/6` in the SLOT STATS line | AIMD budget (§13) |
| No branches produced | `WON'T SUBMIT BRANCH` warnings, with the reason | Health, self-attachment latency, or connectivity (§13) |
| memDAG growing without bound | `proxima_memDAG_numVerticesGauge` vs 50,000 | Size backstop about to force-detach (§12) |
| Node exits under load | `memory stress` in the shutdown reason | Memory watchdog (§15) |
| Node exits at startup or on a gap | `depth cap … forward sync disabled` | No `sources` configured (§5.2) |

`proxima_glb_attachment_cost_counter` accumulates the attachment cost of
finished sequencer attachments — the closest measure of how much work past cones
are costing.

