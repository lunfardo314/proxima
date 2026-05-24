# Branch issuance cost

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

Let `P` denote the bytes of the producing transaction `T` with the bonus field on the stem
output replaced by zero. Enforce on the stem output:

```
B = (blake2b(P) mod M) + 1
```

Remove the VRF signature from the stem output — no longer needed.

`B` becomes _mineable_. Let `K` denote the number of mining iterations beyond the free
baseline:

- `K = 0`: no mining. One free evaluation of the formula on `T`, uniform on `[1, M]`,
  `E[B] = M/2`.
- `K > 0`: vary a nonce, re-sign, re-hash, keep the best `B` seen across `K + 1` samples.
  Then `E[B] = M · (K + 1) / (K + 2)`, which approaches `M` slowly — the remaining distance
  `M − E[B] = M / (K + 2)` halves only when `K` roughly doubles. This is the "explosive"
  character of the marginal cost.

Each mining attempt requires a fresh ED25519 signature, which dominates per-attempt cost
(~50× a blake2b). This is a deliberate property: signing is much harder to accelerate with
specialized hardware than pure hashing, blunting the standard PoW path to ASIC
centralization.

### Validation

Validation is a single recomputation: zero the bonus field in `T`, hash, `mod M`, add 1,
compare against the claimed `B`. Cheap, deterministic, expressible in pure EasyFL.

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

- Enforce `B = (blake2b(P) mod M) + 1` on the stem output in pure EasyFL.
- Remove the VRF signature element from the stem output.
- The current sequencer does not need a mining loop; mining is an optional, sequencer-side
  change that can be added later without further protocol work.
- Reducing per-branch DB-commit cost (delta compression, batched flushes, async commit) is
  complementary and worth tracking as a separate implementation item — not a substitute for
  the throttle.
