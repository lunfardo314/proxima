# Branch issuance cost

> **QUEUED → `sequencer/README.md`** — What issuing a branch costs, and what that implies for sequencer strategy.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

## Context

Branch inflation bonus on the stem output is currently required to equal
`(blake2b(VRF) mod M) + 1`, where `VRF` is a signature over values deterministically
derived from the branch's past cone. `M = 5_000_000` in steady state (different during
bootstrap, but the principle is unchanged).

The lottery has two intended properties:

- **Metastability-breaking randomness.** In the next slot, sequencers prefer to build on
  the branch with the largest projected coverage, computed as
  `coverage + stem_predecessor_coverage / 2`. The random component breaks ties and prevents
  oscillating choices.
- **Uniform per-issuer distribution.** Every sequencer that issues a branch has the same
  expected reward, drawn uniformly from `[1, M]`.

Security does not depend on who wins the lottery — the biggest-coverage rule plus past-cone
determinism take care of that.

## The problem

The marginal cost to a sequencer of issuing a branch is close to zero. Every sequencer
therefore issues a branch in every slot: there is no reason not to.

Each branch transaction has a real cost on every node in the network: committing the slot's
ledger-state delta to the database. With `N` sequencers, per-slot cost grows as `O(N)` on
each node, while each sequencer's expected reward shrinks as `O(1/N)`.

The number of sequencers is itself a decentralization metric and we do not want to cap it.
What we need is a mechanism that lets each sequencer decide, locally and rationally,
whether to compete for a branch in a given slot.

## Model of the solution

Introduce a marginal cost `C` for issuing a branch (ordinary milestones are unaffected).
Make the bonus `B` a random but probabilistically monotone function of `C`: by spending more
effort, a sequencer can grow `B`, but `B` is capped at `M`, so the marginal cost of an
additional token of `B` grows explosively as `B → M`.

This sets up a PoW-style race for branch bonus with a natural equilibrium: when the expected
reward (already `O(1/N)` due to competition) drops below the marginal cost required to
outcompete peers — i.e. when `E[B] < C(B)` — rational sequencers stop pursuing branches in
that slot while continuing to issue ordinary milestones. The number of branches that
*actually compete* for the slot becomes self-throttling, controlled by how many sequencers
find the capex investment worthwhile.

`C` takes the form of CPU cycles spent searching for a better `B`, plus the increased risk
that a slower branch is orphaned by a faster peer. Ultimately this translates into capex on
sequencer hardware.

The dominant motivation for mining is **winning**, not earning a few tokens more: a higher
`B` raises the issued branch's own coverage, which raises the branch's win probability under
the biggest-coverage rule. The capex/luck competition is over *being the branch the network
adopts next slot*, with the bonus as the prize.

## Particular solution

Both the **nonce** and the **mining signature `S`** are carried as inline-data entries
in the transaction-level constraints tuple (`TxConstraints`), outside any output:

- `TxConstraints[0]` = `InlineDataBytecode(nonce_bytes)` (8-byte big-endian `u64`;
  `0` means "no mining baseline"; mining iterates `nonce++`)
- `TxConstraints[1]` = `InlineDataBytecode(S_bytes)` (raw 64-byte ED25519 signature)

Let `P` denote the canonical pre-image: the bytes of the producing transaction `T` with
four fields replaced by `0x`:

- the **whole sequencer output** (many fields B-dependent — see below),
- the **whole stem output** (its `stemLock` carries `slotInflation`/`TotalSupply` which are
  B-dependent in the canonical sequencer flow; zeroing lets the sequencer fill them
  correctly after `B` is known),
- `TxConstraints[1]` (the mining signature `S`), and
- the transaction signature (`TxSignatureData`).

The sequencer output is zeroed because multiple fields on it depend on `B` — token balance
(`amounts[0] = pred + B`), inflation (`amounts[1] = B`), chain constraint's
`cumulativeBranchBonus` (`pred + B`). Zeroing the whole output removes all B-dependent
fields in one cut; chain-identity is still bound to `P` via the sequencer's pubkey through
`S` and via the other tx data (inputs, timestamp, other produced outputs).

The supply-recurrence check (`TotalSupply_new == TotalSupply_pred + slotInflation`) inside
`stemLock` is **kept** in the off-chain attacher (which has full past-cone visibility) but
**dropped** from the on-chain EasyFL constraint. The sequencer rebuilds the stem output
*after* computing `B` so the stem aggregates match what the attacher recomputes.

Let `S = sign(P)` — a deterministic ED25519 signature over `P` by the sequencer's
controller key. Enforce on the stem output:

```
B = (blake2b(P || S) mod M) + 1
```

The transaction signature itself is unchanged from today: it signs the standard tx essence,
which contains `B`, `S`, and the nonce in their final positions. `S` is a *separate*
signature carried in the tx alongside the nonce and verified independently.

Remove the VRF signature from the stem output — no longer needed. The former VRF position
in the stemLock inline data carries the **nonce**. The **mining signature `S`** is added
as a separate constraint on the stem output at constraint index 3 (sibling to the stemLock
at index 2), bumping the stem output's constraint count from 3 to 4. This lets `S` be
zeroed via a single output-tuple-level `replaceTupleElement` rather than requiring a
bytecode-level replacement primitive.

`B` becomes _mineable_. Let `K` denote the number of mining iterations beyond the baseline:

- `K = 0`: no mining. One evaluation — compute `S = sign(P)` for the default nonce, derive
  `B`. The result is uniform on `[1, M]` with `E[B] = M/2`.
- `K > 0`: vary the nonce, recompute `P`, recompute `S = sign(P)`, recompute `B`, keep the
  best `(nonce, S, B)` seen across `K + 1` samples. Then `E[B] = M · (K + 1) / (K + 2)`,
  which approaches `M` slowly — the remaining distance `M − E[B] = M / (K + 2)` halves
  only when `K` roughly doubles. This is the "explosive" character of the marginal cost.

Each iteration requires a fresh ED25519 signature for `S`, which dominates per-attempt cost
(~50× a blake2b). This is a deliberate property: signing is much harder to accelerate with
specialized hardware than pure hashing, blunting the standard PoW path to ASIC
centralization. Total signing cost for a branch is `K + 2` signatures: `K + 1` for the
mining loop and one final tx signature once the best `(nonce, S, B)` triple is fixed.

### Validation

Three checks:

1. Verify the transaction signature over the standard tx essence — unchanged from today.
2. Construct `P` by zeroing the three carve-out fields; verify `S` is a valid ED25519
   signature of `P` under the sequencer's controller key.
3. Verify `B == blake2b(P || S) mod M + 1`.

All three are deterministic and expressible in EasyFL. Constructing `P` from `T` requires
a small embedded helper, `replaceTupleElement(tupleBytes, index, newValue)`, used to zero
each of the four carve-out fields.

### Lazy commitment is the actual throttle

The EasyFL rule enforces *validity* only; it does not — and cannot — prevent a sequencer
from issuing a `K = 0` branch every slot. The cost throttle is supplied by **lazy
commitment**: a branch is committed to the database only when a later sequencer attacher
requests it as a baseline. Lagging branches (low `B`, low coverage) are rarely picked and
therefore rarely persisted.

Sequencer behaviour is dynamic-coverage-maximizing: each new milestone is built on whatever
currently maximizes coverage, so stubbornly extending one's own losing branch is itself a
losing strategy. Combined with lazy commitment, this means that under-mined branches mostly
go unreferenced and unpersisted.

Orphaned branches still impose *some* cost — the per-slot node cost remains `O(N)` — but
the multiplicative constant is small, because most orphans never reach the commit path. The
throttle is therefore probabilistic rather than absolute, but small-constant-`O(N)` is
exactly what we wanted.

The optimal sequencer strategy under these dynamics is **open and subject to modelling**
— mostly conjecture at this stage. Two things keep this from being a security concern:
branch inflation bonus is a small absolute reward, and the question is *spam-sensitive*
rather than security-sensitive — a sequencer that chooses badly loses a little revenue and
wastes a little of the network's commit budget, nothing more.

### Distributional effects

The change is *not* a fairness/throttling tradeoff. It brings branch bonus capture in line
with the rest of Proxima's economics, which are uniformly **permissionless economic
fairness** — free tradeoffs between costs and gains rather than per-participant equality:

- Chain inflation is earned proportionally to holdings (not equal per sequencer) — fair
  because participation is permissionless and reward tracks stake.
- Cooperation on the biggest-coverage rule is itself a prerequisite to participate at all:
  a sequencer with too little coverage has no chance in the lottery regardless of effort.
- Sequencers now also compete for the branch bonus on capex and luck. The freedom to choose
  how much capex to spend is the fair part.

The uniform per-issuer lottery was the anomaly; the new design removes it.

### Macro equilibrium

Proxima sells decentralization as a property. Capital and influence concentration — a
centralization trend, of which capex-heavy branch mining is one form — should translate,
via the market, into a lower token price. The market price is what balances decentralization
against centralization pressure; the protocol does not try to enforce that balance directly.

Branch inflation bonus is intentionally tail inflation: quasi-constant in absolute terms
(like chain inflation), with a share of supply that shrinks over time. Its real value — the
security budget for branch issuance — tracks the token price, which is exactly the coupling
we want.

## Implementation

### Protocol

- Enforce `B = (blake2b(P || S) mod M) + 1` on a branch tx in pure EasyFL, with `S`
  verified as a valid ED25519 signature of `P` under the sequencer's controller key.
- Remove the VRF signature element from the stem output entirely. `stemLock` goes from
  9 args back to 8 (drops the former VRF/nonce slot).
- Place **nonce** and **mining signature `S`** as inline-data entries in `TxConstraints`:
  - `TxConstraints[0]` = nonce (z64; empty when not mining)
  - `TxConstraints[1]` = `S` (raw ED25519 signature; always present on branch txs)
- Stem output stays at 3 constraints. No structural change to outputs.
- Drop the supply recurrence in `stemLock` (`TotalSupply_new == pred + slotInflation`).
  `slotInflation` and `TotalSupply` become trustless analytics; sequencers fill them with
  B-independent values.
- Add an embedded function `replaceTupleElement(tupleBytes, index, newValue) → tupleBytes`
  used by the EasyFL validation code to construct `P` from the producing tx (out-of-bounds
  index is a validator-side fatal error; `newValue` may be empty).

### Mining loop (sequencer-side)

Mining is implemented as part of `txbuilder_seq` transaction creation.

- Iteration loop: increment the nonce inline-data constraint, recompute `P`, compute
  `S = sign(P)`, compute `B = blake2b(P || S) mod M + 1`, keep the best `(nonce, S, B)`
  seen so far.
- After the loop ends: place the best nonce, `S`, and `B` into the tx; sign the standard
  tx essence to produce the final transaction signature.
- Exit predicate: the caller supplies a closure
  `func(iteration int, inflationBonus uint64) bool` — returning `true` ends mining and the
  best `B` seen is published.
  - The closure is invoked only when a new best supersedes the previous one.
  - `iteration` is the iteration count at which the latest improvement occurred (not the
    total iteration count so far).
  - `inflationBonus` is the best `B` seen so far; it is **strictly increasing** across
    successive invocations.
  - A closure that always returns `true` means *no mining* — the first sample is published.

### Sequencer configuration

Reasonable, non-exclusive configuration knobs that translate into exit closures:

- **No mining.** Publish the first sample.
- **Max iterations.** Stop after a configured number of iterations regardless of result.
- **Target threshold.** Stop when the best `B` reaches or exceeds a configured value.
- **Time budget.** Stop after a configured wall-clock duration.

Knobs may be set simultaneously; mining stops as soon as *any* configured condition fires
(the combined closure is the OR of the individual stop predicates).

### Test suite and benchmarks

- Unit tests for the closure-driven mining loop: no-mining path, max-iterations exit,
  threshold exit, time-budget exit, and combinations.
- Benchmarks that simulate mining and print summary statistics — e.g. iterations and wall
  time required to reach the last 1%, 0.1%, 0.01% of `M`. The output is used to calibrate
  reasonable defaults for the sequencer config knobs against measured per-iteration cost.

### Complementary work (not required by this spec)

Reducing per-branch DB-commit cost (delta compression, batched flushes, async commit) is
worth tracking as a separate implementation item. It is complementary to the throttle, not
a substitute.

## Main finding (session 2026-05-25)

After landing PR 1 (`9132ad84` — mineable bonus protocol bedrock, no mining loop yet) and
the canonical-P arg-threading optimization (`2942f4b2`), the design's core weakness became
explicit:

**Mining optionally *increases* a sequencer's cost for issuing a branch — it does NOT
prevent a malicious sequencer from spamming the network with `K = 0` branches that
knowingly fail.**

Concretely: a spammer pays one signature per branch (no mining iterations, no capex),
issues a `K = 0` branch with a uniformly-random `B = M/2` expected, knows it will almost
certainly lose the lottery, but the branch still costs every node in the network:

- parse + Stage 1 / 2 validation,
- attachment to memDAG (Stage 3),
- past-cone solidification,
- the entire constraint pipeline including canonical-P construction and ED25519 verify.

Lazy commitment (the actual DB-write throttle in the original argument) does kick in: the
spam branch never gets referenced as baseline and never reaches the DB. **But all the
work up to the DB-commit happens on every node, with the multiplicative constant only
"small" in the average case** — under adversarial load with N malicious sequencers the
constant matters and the cost is genuinely `O(N)`.

So the equilibrium argument ("when `E[B] < C(B)` rational sequencers skip") only governs
*rational* sequencers. Irrational / adversarial ones can spam at near-zero cost because
the protocol-level mining cost is *opt-in*.

### Where this leaves the design

Two paths:

1. **Leave the design as is.** Accept that protocol-level cost is opt-in and that
   spam-resistance comes from off-protocol mechanisms (peer-level rate limiting, sender
   reputation in `txsenders`, memDAG GC). The mineable-bonus design then serves only as a
   competitive layer for *honest* sequencers seeking higher branch bonus — a fairness
   refinement, not a spam control. Question: is the added complexity (canonical-P
   construction, extra ED25519 verify, dropped supply recurrence, trustless-analytics
   stemLock fields) worth that narrow benefit?

2. **Make the cost mandatory.** Replace the opt-in capex race with an obligatory delay
   that every branch tx must demonstrate. A **VDF** (Verifiable Delay Function) is the
   natural primitive: takes wall-clock `T` time to compute, trivial to verify, cannot be
   parallelised away by capex. Every branch tx carries a VDF proof of `T` over (e.g.) the
   canonical pre-image plus a slot-bound seed; validators reject branches without a fresh
   proof. This bounds branch issuance rate by wall-clock, not by sequencer count or stake.
   Cost: VDF setup and parameter governance are non-trivial, and validation cost (though
   cheap per branch) accumulates network-wide.

A possible middle option — proof of stake-bound rate (e.g. one branch per slot per
stake-weight quantum) — is in principle simpler but reintroduces a permissioned feel that
contradicts Proxima's permissionless-economic-fairness narrative.

The choice is open. The work to date (commits `e2724613`, `cbf4cf82`, `9132ad84`,
`2942f4b2`, `97cfc1a7`) is fully revertible as a single contiguous range if option 1 is
rejected and a VDF or other mandatory-cost design supersedes it.

> **Decision (2026-05-25):** the PR 1 code was reverted on `develop08`; the spec above is
> kept for the historical record. The implementation lives in the git history at the
> contiguous range above and can be cherry-picked back if a future direction requires it.

## Lessons learned

Empirical findings from designing and implementing PR 1 that should inform any
follow-on attempt:

- **B-circularity is a cascade, not a single field.** Each design iteration uncovered
  another field that depended on `B` and therefore had to be carved out of `P`:
  inflation amount → token balance (`pred + B`) → `chain.cumulativeBranchBonus`
  (`pred + B`) → `stemLock.slotInflation` (past-cone + B) → `stemLock.TotalSupply`
  (transitive via `slotInflation`). The final design zeroed **both produced outputs in
  full** (seq output and stem output) to break the cascade in one cut. Any future
  design with a B-derived field must trace the dependency closure end-to-end before
  picking a pre-image shape.

- **Pure-EasyFL canonical-P construction works**, but only by zeroing whole tuple
  elements with `replaceTupleElement`. Zeroing a single inline-data arg inside a
  bytecode-wrapped constraint would have required a new bytecode-aware primitive; we
  avoided that by structurally moving the auxiliary fields (nonce, mining signature
  `S`) out of `stemLock` into `TxConstraints[0]` / `TxConstraints[1]`.

- **Move tx-level auxiliary data into `TxConstraints`.** Whenever a value is logically
  tx-wide rather than output-specific (e.g. mining nonce, mining signature), putting it
  as an `InlineDataBytecode` entry in `TxConstraints` with a documented index is
  cleaner than nesting it inside an output's lock or constraint inline args. Tuple-
  level zeroing for pre-image construction is then trivial.

- **`stemLock` cannot enforce the supply recurrence in a mineable world.** The
  recurrence `TotalSupply_new == TotalSupply_pred + slotInflation` requires
  `slotInflation` to include this tx's `B`, which makes both fields B-dependent.
  Dropping the on-chain check (treating them as trustless analytics) was the only
  workable path; the off-chain attacher (`core/attacher/check.go`) still cross-checks
  to recover the invariant. Future designs should consider whether the off-chain
  attacher should also relax that check, or whether stemLock should reorganise its
  fields entirely.

- **EasyFL `if` is lazy; arg-binding gives memoisation.** A reference to an expression
  is re-evaluated on each occurrence; binding the expression to a function argument
  evaluates it once. This was used in the `2942f4b2` optimisation to compute
  `canonicalP` once per chain constraint and thread it via a new `$7` arg. It is the
  general pattern for cross-call-site sharing inside one constraint evaluation.

- **Sequencer-side build needs a two-pass shape.** Because `B` can only be computed
  after most of the tx is laid out, the sequencer must (a) build outputs with `B = 0`
  placeholders, (b) serialize to derive `P`, (c) sign for `S`, (d) compute `B`, (e)
  rebuild the affected outputs (`seq output` carries `B` in amounts and the chain
  constraint; `stem output` carries `slotInflation`/`TotalSupply`) and patch in the
  real `S` at `TxConstraints[1]`. The PR 1 implementation handled this via direct
  manipulation of `TxBuilder.TxData.OutputBytes[idx]`. A future implementation can use
  the same shape.

- **The 96 ms self-attachment latency threshold is pulse-cycle-related, not raw
  validation work.** The throttle warnings observed on factory tests
  (`TestFactoryNonDecreasingCoverage`) reflect a pulse cycle equal to
  `pace × tickDuration = 96 ms`. The canonical-P optimisation `2942f4b2` cut
  inner replacements from ~60 to ~24 per genesis-distribute tx (4× reduction, verified
  by a counter test) but did not move the throttle latency at all. Profile-driven perf
  work on canonical-P is therefore largely a red herring; the throttle parameter or
  the surrounding pulse logic is the real lever.

- **Mining as an opt-in protocol cost does not solve spam.** This is the most important
  finding and is detailed under "Main finding" above. Adversarial sequencers issuing
  `K = 0` branches pay one signature per branch and cost every node the full parse +
  validate + attach pipeline. Lazy commitment skips the DB write, but not the
  in-memory work. Any future design intended to bound branch-issuance rate must make
  the cost **mandatory**, not opt-in.

## Future directions

Concrete next-step options for the branch-issuance-cost problem, in rough order from
"do nothing" to "deeper protocol change":

1. **Stay with VRF.** Current state of `develop08` after this revert. Per-issuer bonus
   is uniform; no protocol-level cost differentiation; spam-resistance is entirely
   off-protocol (peer-level rate limiting, sender reputation in `txsenders`, memDAG
   GC). Simplest and least-controversial; the problem statement ("marginal cost of
   issuing a branch is close to zero") remains unsolved.

2. **Resurrect the opt-in capex race (PR 1).** Cherry-pick the
   `e2724613..97cfc1a7` range back. Useful if the team decides that capex-driven
   bonus differentiation is worth pursuing as a fairness feature even though it does
   not solve spam. Would still need the perf clean-up and the PR 2 mining loop.

3. **Mandatory cost via VDF (Verifiable Delay Function).** Each branch tx carries a
   VDF proof of duration `T` over a deterministic seed (e.g. branch-tx-essence-hash +
   slot). Validators reject branches without a fresh proof. Properties:
   - bounds branch-issuance rate by wall-clock, not by capex or stake;
   - parallelisation-resistant by construction (the defining property of a VDF);
   - verification is cheap (logarithmic in `T`).
   Open questions: VDF parameter governance (changing `T` over time), library /
   implementation choice (Wesolowski vs Pietrzak; class-group setup), accommodating
   small slot durations (Proxima's slot is ~10 s — `T` must fit comfortably inside).

4**Cheaper branches (orthogonal).** Reduce per-branch DB-commit and validation cost
   so that even with `O(N)` branches per slot the cost stays bounded:
   - state-delta compression,
   - batched / async commit,
   - canonical-P-style EasyFL paths reduced or moved off the per-tx hot path,
   - profile-driven tuning of the sequencer self-attachment latency threshold (which,
     per the lesson above, is the actual bottleneck on the current default tick rate).
   Independent of which spam-control direction is chosen. The original spec already
   notes this as complementary work.

Decision left to a future session.
