# Tick duration — analysis and a rough consolidation model

> **QUEUED → `overview/consensus.md`** — Should a tick be 80, 100 or 120 ms? Analysis and a consolidation model; no code change proposed.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

Status: analysis / decision support. No code change proposed yet.
Question: should the tick duration change from **80 ms** (slot = 10.24 s) to
**100 ms** (12.8 s) or **120 ms** (15.36 s)?

> Math is written for MathJax (`$…$`, `$$…$$`). Renders on the docs site; GitHub's
> plain markdown view will show the raw TeX.

---

## 1. Current parameters (grounding)

| Quantity | Value | Source |
|---|---|---|
| Ticks per slot $K_t$ | **128** (7-bit tick field, `MaxTickValue=0x7f`) | `ledger/base/ledger_time.go:20-22` |
| Tick duration $\tau$ | **80 ms** (default) | `ledger/def_constants0.go:43` |
| Slot duration $T = K_t\tau$ | **10.24 s** | `Constants.SlotDuration()` |
| Sequencer pace $p$ | **12 ticks** = $p\tau$ = 0.96 s | `ledger/def_constants0.go:46-47` |
| Max endorsements $E$ | **8** | `constMaxNumberOfEndorsements` |
| Health threshold $C_h$ | **7/12 ≈ 0.583** | `Default…CoverageNumerator/Denominator` |
| Consensus threshold $C$ | **≥ 2/3** (supermajority by coverage) | cooperative-consensus rule |
| Self-attach tolerance | **12 ticks** = 0.96 s | `sequencer/strategy_async.go:24` |

The tick field is fixed at 7 bits, so $K_t=128$ is **immutable**; the slot scales
linearly with the tick: $T(\tau)=128\,\tau$. Candidates:

| $\tau$ | slot $T$ | rounds/slot $K=T/(p\tau)=K_t/p$ | self-attach tol. |
|---|---|---|---|
| 80 ms | 10.24 s | $128/12\approx 10.7$ | 0.96 s |
| 100 ms | 12.8 s | 10.7 (unchanged) | 1.20 s |
| 120 ms | 15.36 s | 10.7 (unchanged) | 1.44 s |

**Key invariant:** the number of milestone rounds per slot, $K=K_t/p\approx 10.7$, is
**independent of $\tau$**. Changing the tick rescales the *wall-clock* of each round, not
their count. This is central to the model below.

---

## 2. What a slot is for

A branch transaction at the slot boundary commits a UTXO-set delta whose **ledger
coverage** (consumed amount in its past cone) should capture the dominating share of
capital. The biggest-coverage rule (LRB) then makes that branch the reliable tip. So a
slot is the **window in which the network must consolidate a supermajority ($\ge C M$)
of capital into one branch's past cone**. If too little capital is reachable within a
slot, no healthy branch ($\ge C_h M$) forms and the chain stalls or forks.

The slot must therefore be long enough for capital that is *physically far apart
(in latency)* to be gathered into a common past cone before the boundary.

---

## 3. The latency metric space

Let the sequencers be nodes $V=\{1,\dots,N\}$, each with **mass** (contributed coverage)

$$m_i = \text{balance}_i + \text{frozenCoverage}_i,\qquad M=\sum_i m_i .$$

Let $d(i,j)$ be the **transitive RTT** — the shortest-path round-trip latency in the
peer graph (sum of per-hop RTTs along the gossip path used to relay a milestone).
$(V,d)$ is a metric space: $d\ge 0$, $d(i,i)=0$, symmetry, and triangle inequality
hold for shortest-path latencies. Define the **ball mass**

$$B(c,R)=\sum_{j:\,d(c,j)\le R} m_j .$$

This is the capital reachable within latency $R$ of a center $c$.

---

## 4. Propagation within a slot → reachable radius $R(T)$

Coverage from node $j$ enters a branch built at node $c$ only after $j$'s milestone
(carrying $j$'s coverage, possibly via intermediate endorsements) has reached $c$.
Two facts bound how far coverage travels in one slot:

- **Round structure.** A sequencer emits a milestone every $p\tau$ seconds and can
  endorse up to $E=8$ peers' latest milestones. In $K=K_t/p\approx 10$ rounds per slot,
  coverage diffuses with branching factor $E$, so by *hop count* it reaches $E^K$ nodes
  — astronomically more than any realistic $N$. **Hop count is never the binding
  constraint**, provided the gossip graph diameter $\le K\approx 10$ hops.
- **Latency budget.** A milestone emitted at the start of a round must arrive at a peer
  before that peer's next milestone (~one $p\tau$ interval) to be endorsed in the next
  round; otherwise it slips to a later round. Chaining $K$ rounds, a node at one-way
  latency $\ell$ from $c$ is incorporated after $\lceil \ell/(p\tau)\rceil$ rounds, so
  **all nodes with $\ell \lesssim K\,p\tau = T$ are reachable**.

Hence the binding constraint is latency, and the **reachable radius** is the slot's RTT
budget minus fixed overhead (build + validate the branch, leave margin for it to be
adopted as LRB):

$$\boxed{\,R(T)\;\approx\; T - T_{\text{ovh}}\,},\qquad
T_{\text{ovh}} \sim \underbrace{p\tau}_{\text{one round}} + \underbrace{\delta_{\text{val}}}_{\text{branch build/val}} + \underbrace{\text{margin}}_{\text{LRB adoption}}.$$

(Using RTT for $d$ folds the gather-in + disseminate-out legs together; if one models
them separately, replace $R(T)$ by $\tfrac12(T-T_{\text{ovh}})$ for a one-way metric.)

---

## 5. The consolidation condition (the conjecture, formalized)

**Conjecture (restated).** Consensus ($\ge C M$ coverage in one branch) is reached within
a slot of duration $T$ iff some center gathers fraction $C$ of capital within $R(T)$:

$$\exists\,c:\quad B\big(c,\,R(T)\big)\ \ge\ C\,M .$$

Define the **consolidation radius** — the smallest RTT-radius that captures fraction $C$
of capital around the best center:

$$\rho_C \;=\; \min_{c\in V}\ \min\{\,R: B(c,R)\ge CM\,\}.$$

The minimizing center $c^\star$ is the capital-weighted **center of mass** in the latency
metric (rigorously, the min-enclosing-mass center; intuitively, the Fermat–Weber /
geometric median weighted by $m_i$). Then the **safety condition** is

$$R(T)\ \ge\ \rho_C
\quad\Longleftrightarrow\quad
T \ \ge\ \rho_C + T_{\text{ovh}}
\quad\Longleftrightarrow\quad
\boxed{\ \tau \ \ge\ \dfrac{\rho_C + T_{\text{ovh}}}{128}\ }.$$

In words: **pick the tick so that one slot's RTT budget covers the latency radius needed
to enclose a supermajority of capital, plus overhead.**

$\rho_C$ encodes all of (a)–(b) the user listed: it grows with latency (the metric $d$)
and with capital dispersion (how spread the $m_i$ are in that metric). A network whose
capital is latency-concentrated has small $\rho_C$ and tolerates a short slot; a globally
dispersed, capital-balanced network has large $\rho_C$ and needs a long slot.

---

## 6. Probabilistic refinement (latency is random)

Latency is a distribution, not a number; what we want is a *small per-slot failure
probability* $\varepsilon$. Let $d(c^\star,j)$ be random and
$p_j(T)=\Pr[d(c^\star,j)\le R(T)]$. The consolidated mass is the random variable
$S(T)=\sum_j m_j\,\mathbb{1}[d(c^\star,j)\le R(T)]$ with

$$\mathbb{E}[S(T)] = \sum_j m_j\,p_j(T).$$

Define the per-slot success probability and require it close to 1:

$$P_{\text{succ}}(T)=\Pr\big[S(T)\ge CM\big]\ \ge\ 1-\varepsilon .$$

Because $S$ is a mass-weighted sum of (correlated) indicators, the **tail of the latency
distribution** dominates: a few high-mass sequencers behind a fat RTT tail can drop
$S$ below $CM$ in a bad slot. So the design target is not the *mean* RTT radius but a
high quantile, e.g. $R(T)\ge \rho_C$ evaluated at the 95–99th latency percentile. This is
why one wants $T$ comfortably **larger** than $\rho_C$, with a safety factor $k$:

$$T^\star \approx k\,(\rho_C + T_{\text{ovh}}),\quad k\sim 5\text{–}10\ \text{(tail margin)}.$$

---

## 7. Coupling to throughput (c) per-sequencer and (d) per-node

- **Per-sequencer consolidation (c).** Merging $N$ sequencers into one past cone needs a
  reduction tree of depth $\sim\lceil\log_E N\rceil$ endorsement layers. Each layer costs
  one round $p\tau$. The merge time floor is therefore
  $$T_{\text{merge}}\approx \lceil\log_E N\rceil\,p\tau + \rho_C .$$
  With $E=8$: $N=100\Rightarrow 3$ layers, $N=1000\Rightarrow 4$. Since the slot affords
  $K\approx 10$ rounds regardless of $\tau$, even $N=10^4$ ($\sim5$ layers) leaves
  $\sim5$ rounds of slack for physical propagation. **Endorsement bandwidth is not the
  bottleneck at any realistic $N$;** latency $\rho_C$ is.

- **Per-node throughput (d).** A full node validates every milestone + branch in real
  time. Each sequencer emits one milestone per $p\tau$; aggregate rate
  $\lambda \approx N/(p\tau)$. Sustaining $\lambda$ requires
  $\delta_{\text{val}}\cdot\lambda \le 1$, i.e. $\tau \ge N\,\delta_{\text{val}}/(p\,K_t)\cdot K_t = N\delta_{\text{val}}/p$… more simply, **longer ticks lower $\lambda$**
  (fewer milestones/second) and widen the self-attachment tolerance ($12\tau$), giving
  the node more slack. So (d) **pushes toward longer ticks** (or is neutral).

**Both (a)+(b) safety and (d) node-load push toward longer slots; only finality latency
pushes toward shorter.** (c) is slack at realistic scale.

---

## 8. The trade-off and the objective

Shorter slots give faster finality (LRB confirms in a few slots) and higher branch
cadence; longer slots give a larger consolidation radius and more node slack. The
decision is a constrained minimization:

$$\tau^\star \;=\; \tfrac{1}{128}\,
\min\Big\{\,T \;:\; P_{\text{succ}}(T)\ge 1-\varepsilon \ \wedge\ \delta_{\text{val}}\,\tfrac{N}{p\tau}\le 1 \,\Big\}.$$

Minimize slot length (fast finality) subject to a small per-slot health-failure
probability and node-processing feasibility.

---

## 9. Side effect to flag — inflation recalibration

Chain inflation is defined **per slot**: `chainInflationOneSlot(A,s)=A/(M0+s)` with
slot index $s$ (`ledger/def/inflation.easyfl`). Annual inflation $\approx$
(per-slot rate) $\times$ `SlotsPerYear`, and `SlotsPerYear` $\propto 1/\tau$. Therefore:

$$\text{tick } 80\to120\text{ ms}\ \Rightarrow\ \text{slots/year}\times\tfrac{2}{3}\ \Rightarrow\ \text{annual chain inflation } \approx 10.2\%\to 6.8\%$$

unless `constSlotInflationBase` (and the branch-bonus tail) are rescaled. **Changing the
tick silently rescales the monetary policy** unless the inflation constants are
recalibrated to hold the target annual rates. This couples directly to the just-shipped
flat-branch-inflation work (`claude/inflation.md`). Any tick change should be paired with
an inflation re-derivation and a re-run of `proxi util inflation_emulation`.

Tick duration is part of the genesis ledger identity, so a change is a **fresh-genesis /
testnet-restart** event, not a hot upgrade.

---

## 10. Rough numbers — current testnet

The 4-machine testnet (`boot`, `loc0`, `seq1`, `loc1`) is geographically dispersed;
order-of-magnitude inter-node RTT $\sim 100\text{–}300$ ms, so even a worst-case
2/3-capital radius is $\rho_{2/3}\lesssim 0.5\text{–}1$ s. Against $T=10.24$ s the margin
is $k\approx 10\text{–}20\times$. **At current scale the slot is already
over-provisioned by an order of magnitude;** 80 vs 100 vs 120 ms makes no observable
safety difference here. The current testnet cannot *empirically* distinguish the
candidates — all three are deeply safe.

So the choice is **not** about today's testnet. It is a bet on the target production
topology: many sequencers, global dispersion, fatter latency tails, and a desire for
larger margin $k$. If $\rho_{2/3}$ (tail) for the target network is believed to be in the
$1\text{–}2$ s range, then $T=10.24$ s already gives $k\approx 5\text{–}10$; going to
15.36 s buys more margin at a flat 50% finality penalty.

---

## 11. Concerns / failure modes

- **Capital concentration risk.** $\rho_C$ depends on the *current* capital map; a single
  large sequencer moving "far" (high RTT, e.g. behind a slow link) can blow up
  $\rho_{2/3}$ overnight. The slot must be sized for the *worst plausible* capital map,
  not the average. This is a liveness, not safety, failure (chain stalls / forks, doesn't
  mint bad money).
- **Partition.** If a partition splits capital so no side holds $C_h M$ within $R(T)$,
  no healthy branch forms on either side — by design (this is what the now-Go-level,
  suppressible health gate enforces; see `claude/…branch_health`). A longer slot tolerates
  larger transient partitions.
- **Finality latency.** $+50\%$ slot $\Rightarrow +50\%$ wall-clock to LRB finality. Real
  UX cost for settlement-sensitive use.
- **Throttle/threshold scaling.** Wall-clock thresholds expressed in *ticks*
  (self-attachment tolerance $=12\tau$, pace) scale automatically; thresholds expressed
  in *milliseconds* in Go (e.g. the historical 96 ms self-attachment warning) do **not**
  and must be re-examined.
- **Inflation drift** (§9).

---

## 12. How to decide (what to measure / simulate)

1. **Instrument the live RTT matrix** $d(i,j)$ (already have peer pings) and the capital
   map $m_i$ (balance + frozen). Compute $\rho_{2/3}$ and its time series / tail.
2. **Sweep**: for $\tau\in\{80,100,120\}$ ms compute $R(T)=128\tau-T_{\text{ovh}}$ and the
   margin $R(T)/\rho_{2/3}$. Pick the smallest $\tau$ with margin $\ge k$ for chosen $k$.
3. **Simulate** the target network (N sequencers, capital distribution, RTT distribution
   with tails) and estimate $P_{\text{succ}}(T)$ directly via Monte-Carlo of the round
   diffusion in §4 — this is the honest version of the closed form.
4. **Pair with inflation re-derivation** (§9) before committing to a genesis.

---

## 13. Limitations of the model

- $R(T)\approx T-T_{\text{ovh}}$ treats diffusion as latency-limited with $\le K$ hops; it
  ignores congestion (queueing at high $\lambda$), which would shrink the effective $R$.
- It assumes a single best center $c^\star$; real consolidation is multi-rooted (several
  sequencers build competing branches), which *helps* (max over centers) but also splits
  effort (coverage races). A coalescence/centroid race model would refine $P_{\text{succ}}$.
- $\rho_C$ is computed on a snapshot of capital; capital and topology drift.
- RTT correlations (shared bottleneck links) are ignored in the i.i.d. tail argument.

**Bottom line.** The model gives a usable rule —
$\tau \ge (\rho_C + T_{\text{ovh}})/128$ with a tail safety factor — and says the binding
quantity is the **RTT radius enclosing a 2/3 supermajority of capital around the capital
center**, not throughput. At current testnet scale all three candidates are far on the
safe side, so the tick choice should be driven by the *target* network's measured/simulated
$\rho_{2/3}$ tail and by the finality-latency budget — and must be paired with an inflation
recalibration. My lean: don't increase $\tau$ without measured/simulated evidence that
10.24 s is marginal for the target topology, because the cost (slower finality, inflation
re-derivation, fresh genesis) is concrete while the benefit at current scale is not.
