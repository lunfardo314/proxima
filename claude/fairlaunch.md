# Fair Launch — the `mine` chain: finalized spec & implementation plan

Status: IMPLEMENTED (merged to `develop`, the testnet branch). Breaking hardfork.
See §6 for the first cut's shipped status, §7 for the earlier flat-`K = B` design (single-
gap retarget + stuck-chain relief valve — sawtoothed on the live net), and **§8 for the
current difficulty design (pace-relieved `K = max(B − (M − P), E)` + ±1 retarget), which
supersedes §7 and is now IMPLEMENTED.** §1-§2 below are the first-cut record. Remaining
deferred: the input-flood filter (§4). The official miner shipped as the in-tree
`proxi node mine` command (§6).
Research and difficulty/contention model preserved in `fairlaunch-research.md`.
Date: 2026-07-08

The goal, legal framing (MiCA / no-issuer), precedents, and the difficulty /
contention model live in `fairlaunch-research.md`. This document is the buildable
spec for the first cut and the file-by-file implementation plan.

Scope of the first cut:
1. `mine(R)` mining policy — implemented entirely inside a single **`mineLock`**
   constraint at the lock slot (index 2).
2. one extra genesis UTXO: the mine-chain output, with a predefined constant chain ID.
3. base tests (ledger-level, `utxodb`).

Deferred (not in this cut): the input-based double-spend flood filter and adaptive
difficulty. (The sender-known-in-LRB spam-filter exemption for mining transactions and
the official `proxi node mine` miner have since shipped — see §6; adaptive difficulty
has since shipped too — see §7. Only the input-flood filter remains.)

---

## 1. Parameters (first cut)

Motes: 1 PROX = 10⁶ motes. Slot τ = 10.24 s (128 ticks × 80 ms); ≈ 3.08 M slots/yr.

| Symbol | Value | Meaning |
|---|---|---|
| I | 10¹⁴ motes (10⁸ PROX) | genesis supply (bootstrap sequencer output) |
| T | 10¹⁵ motes (10⁹ PROX) | mintable ceiling (not a held sum) |
| R_init | T − I = 9×10¹⁴ motes | initial value of the remaining-mintable counter R |
| A | **1000 PROX = 10⁹ motes** | minted per transit (was 500; sized in research §10) |
| N | R_init / A = 9×10⁵ | total transits to exhaust R |
| C | fixed dust, sized to worst-case output bytes | the mine output's own balance, constant forever (see storage-deposit note) |
| B₀ | global const (seed difficulty) | seeds the mutable B at genesis; testnet 24, tests 8 |
| E | global const (floor difficulty) | **now 10** (§7.5); was 22 in the first cut |
| P | global const (min pace, slots) | **now 3** (§7.5); was 1. Tests use 2 so a below-minimum pace is testable |

**Superseded by §7** — the first cut used the difficulty curve
`K(M) = max(B − (M − P), E)`, `M = succ.slot − pred.slot ≥ P`, `K(P) = B`. The
M-dependence is gone: **K = B**, and B is retargeted per transit within a band
[E, C] (§7.3). The `B < 64` requirement stands (the PoW test operates on the low 64
hash bits) and is now enforced by the explicit ceiling `constMineMaxDifficulty`.

Emission (research §9, redone with the inflation term in §10): only A/M̄ — motes per slot
— matters. This paragraph originally assumed A=500 at the then-expected floor pace M̄≈2
(2.5×10⁸ motes/slot) giving a ≈47-day decentralization point and ≈1.17 yr full emission.
The shipped target pace is now 4 (§7), so **A is 1000** to hold that same 2.5×10⁸
motes/slot and the same schedule. Any future change to the target pace must move A with
it. The 50%-crossing is ~47 d and full emission ~1.17 yr; note t_full ≈ 9·t_decentral is
structural (R_init/I = 9), so the deadline and the tail cannot be tuned independently by A.

The premine keeps inflating while mining runs (chain + branch inflation), so miners chase a
moving target and the 50%/33% crossings land *later* than a pure-mining figure suggests.
Research §10 quantifies it from the constraints and folds it into the ~47 d above: at a
one-month horizon it is only a ~1% correction on A, because the mining flow (A/M̄ = 2.5×10⁸
motes/slot) is ~40× the total inflation flow (5.8×10⁶). It matters on year scales, not here.
None of this touches the mine constraint (A and R are fixed); the levers are A (deadline and
tail together) and the I/T split (their ratio) — never `mineLock`.

**Split: fixed policy in global constants, mutable state in lock args.**
- Global ledger constants (A, E, P, B₀, and the retarget params for §4) are configured
  at ledger init exactly like tick duration — so unit tests set them low (B₀=8) and
  each testnet picks its own at genesis, no per-UTXO cost and no library hardfork to
  retune a testnet.
- Lock args carry only the **mutable** per-transit state: `mineLock(R, B, s3, s2, s1)`.
  R is the remaining-mintable counter (decrements by A). B is the current difficulty,
  **seeded from B₀ at genesis** and mutable for adaptive retargeting; in the first cut
  it is static (`B_succ = B_pred`). `s1,s2,s3` are the slot ring (§4), carried and
  rolled every transit but ignored by the first-cut difficulty logic.

**Storage deposit — why C is worst-case sized.** The minimum storage deposit is
`storageDeposit(size)` (size×50000 below 100 B; size×250000−20M above) — a pure function
of the output's effective byte size and fixed constants, **not** supply-relative, so
supply growth never invalidates a fixed C. But the mine output's slot args are z32/z64
(leading-zero-stripped), so `s1,s2,s3` and the predecessor slot each grow 0→4 data bytes
as slots climb toward `MaxSlot` — up to +16 bytes over the chain's life; R is widest at
genesis and only shrinks. So C is fixed at genesis to the **worst-case** effective size
(all slot args at 4 bytes, R at genesis width) → `storageDeposit(that)`; the extra reserve
is small (≤ ~4M units above the kink) and then C satisfies the floor forever. Exemption
(the stem/tagAlong/SWD allowlist) is *not* used: those are justified by bounded lifetime,
which the permanent mine UTXO lacks — exempting it would reopen the dust vector.

---

## 2. The mine chain — finalized semantics

A single chained UTXO fixed at genesis. Its lock is `mineLock`; everything the mining
policy needs is enforced inside that one constraint. The chain constraint at index 3
is kept for ChainID preservation, predecessor/successor linkage and the transition
counter — `mineLock` rides on top of it.

**Readability is a first-class requirement.** This constraint is public and will be
read by everyone who wants to mine. Write it as small, well-named predicates
(`_boundToMineChain`, `_validPace`, `_validRDecrement`, `_validPayout`, `_powOK`)
with a one-line comment per rule; no clever fragile encodings.

Output tuple of the mine chain UTXO:

| idx | content |
|---|---|
| 0 | amounts: balance = C (constant), **inflation = A** on the produced successor |
| 1 | index-values (mining params / empty as needed) |
| 2 | `mineLock(R, B, s3, s2, s1)` — open unlock + full mining policy |
| 3 | `chain(...)` — standard chain constraint (unchanged args) |

`mineLock` enforces a **fully static, known-in-advance transaction template**. All of it
is checked on the **consumed (predecessor) arm**, which always runs when the mine UTXO is
spent — so the template cannot be evaded (in particular a successor that dropped
`mineLock` is rejected here). The produced arm only self-pins the lock position; the
predecessor governs the whole transition.

**Exact transaction shape** — one input (this mine UTXO), exactly three outputs, nothing
else:

- **`[0]` mine successor** — three constraints only (index-values empty): amounts
  (balance = C unchanged, inflation = A), `mineLock(R−A, B, rolled ring)`, `chain(...)`
  continuing `MineChainID`. Requiring the successor lock to be `mineLock` is what keeps
  the chain permissionlessly mineable forever — otherwise it could be captured by a
  `sigLock` and mining would end.
- **`[1]` payout** — a sig-locked output whose target holder ID equals the tx signer's
  holder ID, amount `A' ≤ A`.
- **`[2]` tag-along** — amount `T = A − A'`, capped `T ≤ 1% of A` (rule 5).

**Policy enforced on that template:**

1. **Bound to the one chain.** The predecessor's own sibling chain constraint (index 3)
   ChainID must equal `MineChainID` (genesis constant) — so `mineLock` validates on no
   other output/chain (same "read the sibling chain constraint" pattern the foundry uses).
2. **Open unlock.** No per-output signature (stem-lock precedent); the one mandatory tx
   signature (`tx_integrity_validator.easyfl`) supplies the holder ID targeted by `[1]`.
3. **R decrement / terminal.** `R_succ = R_pred − A`; if `R_pred < A` no valid successor
   exists — the mine chain has ended.
4. **Difficulty retarget.** `B_succ = B_pred` in the first cut; **now
   `B_succ = _mineAdjustedB(B_pred)`** — one bit per transit within [E, C] from the single
   last gap `txSlot − predSlot`, see §7.3. The slot ring was dropped; the lock is now just
   `mineLock(R, B)`. A, E, C, P and the target pace are global constants, not carried.
5. **Fee cap.** The `T ≤ 1% of A` cap blocks the outsourcing attack: without it a miner
   could route almost all value as fee to a sequencer it controls, keep `A'` negligible,
   and safely pool/hand off its signing key. Forcing ≥99% into the key-locked `[1]` means
   outsourcing the key risks the real reward.
6. **Inflation.** `[0]` declares inflation = A; conservation balances automatically
   (consumed C; produced C + A' + T = C + A). No change to `validateOutputs`; the chain
   constraint's per-output inflation cap is relaxed for `#mineLock` (plan §3.3).
7. **PoW (pure EasyFL).** `blake2b(txBytes)` — hash of the whole signed tx, signature
   included — must end in `≥ K(M)` zero bits, `K(M) = max(B−(M−P), E)`,
   `M = succ.slot − pred.slot ≥ P`. Low-64-bit test:
   `equal( lshift64(rshift64(h64,K), K), h64 )`, `h64 = tail(blake2b(txBytes),24)`. The
   miner varies a **nonce in `mineLock`'s unlock parameters** (free-form, ignored by the
   open unlock, part of the essence) → new txid → new deterministic signature → new hash.
   Every attempt signs the tx → *proof-of-signing-work*: CPU-egalitarian, ASIC-hostile,
   non-outsourceable.

Notes carried from the research (still true, not re-argued here):
- the pace M is the miner's choice, fixed into the successor timestamp — a solution
  cannot be reused at a different M;
- several miners may mine valid successors for the same predecessor; those are
  double-spends, resolved by coverage; the difficulty policy keeps their count low;
- the future-timestamp rule (a tx timestamped in the future is rejected, not buffered)
  is an existing enforced node invariant — verify, don't re-implement.

---

## 3. Implementation plan (file by file)

### 3.0 Mine policy constants — alongside the existing ledger constants (e.g. tick duration)

Add `A`, `E`, `P`, `B₀` (and, reserved for §4, the retarget params: target pace, cap,
adjust step) as ledger constants configured at ledger init, the same mechanism tick
duration uses. This is what lets unit tests set B₀=8/E=4 and mine instantly, and lets
each testnet pick its difficulty at genesis. Expose them to EasyFL as named 0-arg
constants (like `tickDuration`) so `mineLock` reads `A`, `E`, `P` directly.

### 3.1 Genesis: the second chain output — `ledger/base/genesis.go`, `ledger/genesis.go`, `ledger/multistate/genesis.go`

- `ledger/base/genesis.go`:
  - add `GenesisMineChainOutputIndex = byte(3)` and `GenesisMineChainOutputID() =
    MustNewOutputID(GenesisTransactionID(), 3)`.
  - **bump `GenesisTransactionIDShort()` `ret[0]` from 2 to 3** (max produced output
    index). This changes `GenesisTransactionID()` → `GenesisOutputID()` → the bootstrap
    sequencer chain ID. Recompute and update `BoostrapSequencerIDHex` (here **and** the
    duplicate in `ledger/genesis.go`).
  - add `MineChainIDHex` + `var MineChainID ChainID`; in `init()` assert
    `MineChainID == MakeOriginChainID(GenesisMineChainOutputID())`.
  - obtain both hex values by a one-off print of `MakeOriginChainID(...).StringHex()`
    after the `ret[0]=3` bump.
- `ledger/genesis.go`: add `GenesisMineChainOutput()` mirroring `GenesisOutput` but
  non-sequencer: `WithLock(mineLock(R_init, B₀, 0, 0, 0))` (ring seeded to zero),
  `PutConstraint(NewChainOrigin(0).Bytes(), ConstraintIndexChain)`, ChainID = MineChainID.
  Balance C = `storageDeposit(worst-case effectiveStorageSize)` — build the output once
  with all four slot args forced to their 4-byte max, take that effective size, and fund
  C to its deposit (see §1 storage-deposit note). C stays constant across every transit.
- `ledger/multistate/genesis.go`: in `InitStateStoreFromGlobals` create the mine
  output and add it in `genesisUpdateMutations`; extend `unspent.InsertRange(0, 3)` to
  `InsertRange(0, 4)` (5 real outputs 0..4 — wait: current 0..2 are boot/stem/dust,
  index 3 becomes mine; upgrade UTXO stays synthetic at 255). Adjust `ScanGenesisState`
  expectations (`genesis.go:117-127`).

### 3.2 `mineLock` constraint — new `ledger/def/lock_mine.easyfl` + `ledger/lock_mine.go`

- EasyFL body per §2, as named sub-predicates. Args `$0=R $1=B $2=s3 $3=s2 $4=s1`
  (A, E, P read from global constants — §3.0). Pin position
  `require(equal(selfBlockIndex, lockConstraintIndex), …)`. Open consumed arm (no
  `validSignature`, no `_sigLock`). Produced arm: `R_succ==R_pred−A`,
  `selfInflationAmount==A`, successor shape, **B carry** (`B_succ==B_pred`, static
  first cut) and **ring roll** (`s1_succ==pred.slot`, `s2_succ==s1_pred`,
  `s3_succ==s2_pred`). `_powOK` and `_validPace` read `txBytes`, `txSlot`, and the
  predecessor input's slot; the difficulty uses B (from args) and E/P (constants),
  ignoring the ring in the first cut.
- Go side mirrors `lock_stem.go` / `lock_signature.go`: a `MineLock` struct
  (R, A, B, E, P), `MineLockFromBytes`, `Bytes()`, registration with the right arity,
  and a `#mineLock` lock-type predicate so `selfHasLockType(#mineLock)` works.
- No new Go builtin (PoW is pure EasyFL). `blake2b`, `txBytes`, `lshift64`, `rshift64`,
  `tail`, `equal` already exist.

### 3.3 Relax the chain inflation cap for `#mineLock` — `ledger/def/chain.easyfl`

- Both lock-specific inflation carve-outs (the `#mineLock` inflation-cap bypass and the
  `#delegateLock` frozen-coverage bypass) live **inside** `_validInflationAmount`, with a
  single `_validInflationAmount($0,$1,selfInflationAmount)` call at the site. The mine
  chain does not use cumulative-inflation accounting; its inflation bound is
  `selfInflationAmount == A`, enforced by `mineLock`.
- While here, fix the stale "index 2" comments/error string (chain is at index 3).

### 3.4 Build recipe — `ledger/txbuildercore/helpers_mine.go` (new)

- `NewMineLockBytecode(R,A,B,E,P)`, and a `NewMineTransition(...)` helper that, given
  the predecessor mine output and a nonce+pace, assembles the successor mine output
  (index 0), the siglock payout (index 1) and the tag-along (index 2), sets inflation A,
  and puts the nonce into `mineLock`'s unlock params. Needed by the tests (and later the
  external miner). Follows the delegation/foundry recipe style.

### 3.5 Base tests — `ledger/tests/mine_test.go` (new)

Use `utxodb` with a genesis built at low difficulty (B=8, E=4, P=1) so mining is
instant. Cover:
- happy path: one transit — R decrements by A, inflation A declared, payout A' at [1],
  tag-along T at [2], successor balance == C, ChainID preserved;
- PoW enforced: a wrong nonce (insufficient trailing zeros) is rejected;
- pace enforced: M < P rejected; K(M) drops with larger M (mine a longer step at lower K);
- fee cap enforced: T > 1% of A rejected;
- terminal state: R_pred < A has no valid successor;
- containment: `mineLock` on any output whose sibling chain ID ≠ MineChainID is rejected.
Add explanatory comments to each test (project rule).

### 3.6 Run

`go test ./ledger/...` (ledger/EasyFL change — no `-race` needed per test-scope rule).
Rebuild `proxima` + `proxi`.

---

## 4. Deferred (explicitly out of this cut)

- **Input-based double-spend flood filter.** The sender-known-in-LRB exemption for
  mining transactions has shipped (§6), so brand-new miners are no longer blocked. Still
  wanted: an input-based flood filter (drop > N unsolicited txs sharing the same input
  per window), because many miners racing the same predecessor produce conflicting
  successors on the one mine-chain input. A workflow/txinput change, needed for live
  mining but not for the ledger constraint or base tests.
- **Adaptive difficulty — implemented in EasyFL** (not Go), architecture (A) stored
  mutable B. The plumbing ships in the first cut (the slot ring `s1,s2,s3` is carried and
  rolled every transit, B is a mutable arg); only the retarget is deferred:
  `adjust()=0`, so `B_succ=B_pred`. Genesis seeds the ring to all-zero, which is inert
  while the difficulty logic ignores the ring.
  **The retarget formula is now designed — see §7**, which supersedes this bullet and
  also drops the difficulty's dependence on the step M and raises P to 3.

---

## 5. Decisions

Resolved:
- **Param split** — A, E, P, B₀ (+ retarget params) are global ledger constants (set like
  tick duration); mutable state (R, B, slot ring) lives in `mineLock` args. (A) stored
  mutable B; ring carried+rolled in the first cut but ignored by the static difficulty.
- **Genesis chain-ID recompute is breaking** — `BoostrapSequencerIDHex` (both copies) and
  the new `MineChainIDHex` change; accepted (this branch is a breaking hardfork).

Still open:
- ~~**Concrete first-testnet B₀/E/P**~~ — RESOLVED in §7.5 (P=3, B₀=24 seed, E=10, new ceiling C=40, target pace 4). Unit tests: B₀=8, band [6,10], P=2.
- **Zero-fee mine tx** — rule 7 caps T at 1% of A but sets no *minimum*; `A'=A, T=0` is
  allowed as written (miner then needs another path to a sequencer). Keep permissive
  unless a minimum tag-along is wanted.

## 6. Implementation status (branch `fairlaunch`, 2026-07-07)

Shipped, `go test ./ledger/...` green:
- **3.0 constants** — `constMineChainID`, `constMineAmount`, `constMineMinPace`,
  `constMineBaseDifficulty`, `constMineFloorDifficulty`, `constMineRemainingInit` in
  `def/def_constants0.json`; `InitParameters.Mine*` fields + defaults (A=1000 PROX,
  B=24, E=22, P=1, R_init=9e14) in `def_constants0.go`; `WithMineDifficulty` test option.
- **3.1 genesis** — mine output at index 3; `GenesisTransactionIDShort` bumped 2→3.
  The genesis chain IDs are fixed, human-readable 24-byte ASCII constants,
  independent of the genesis output IDs: `BoostrapSequencerID="Proxima.bootstrap.chain."`,
  `MineChainID="Proxima.fairlaunch.mine!"` (in `base/genesis.go`, `def_constants0.json`;
  `genesis.go` mirrors the bootstrap hex). The genesis chain outputs carry these as
  EXPLICIT (non-origin) chain constraints — the genesis is inserted directly and
  never validated as produced, so its chain ID can be arbitrary and is simply
  preserved onto the first successor. `GenesisMineChainOutput()`; wired into
  `multistate/genesis.go` + `genesis_snapshot.go` (`InsertRange(0,4)`).
- **3.2 mineLock** — `def/lock_mine.easyfl` (static template, consumed-arm enforced,
  pure-EasyFL PoW via `lshift64/rshift64`, split into ≤15-arg groups) + `lock_mine.go`
  serde, registered arity-5.
- **3.3 chain relaxation** — the `#mineLock` inflation-cap bypass and the `#delegateLock`
  frozen-coverage bypass are both folded inside `_validInflationAmount`, called once from
  `chain` in `def/chain.easyfl`.
- **3.4/3.5 recipe + tests** — the mine-transition builder lives inline in
  `ledger/tests/mine_test.go`; the wallet-side lock helpers (`NewMineLock` /
  `ParseMineLock`) are in `ledger/txbuildercore/helpers_mine.go`. Tests: happy path,
  insufficient PoW, fee-cap, wrong-payout-holder
  (non-outsourceability), pace-below-minimum, difficulty-drops-with-pace, chain-exhausted
  (terminal R < A), and mineLock-only-on-mine-chain (containment). The test ledger uses
  P=2 and R_init=A so the pace and terminal paths are reachable. All pass.
- **Spam-filter exemption** — `Transaction.IsMiningTransaction()` recognizes a mine
  transit (non-seq, 1 input, 3 outputs, mine chain on output 0) and exempts it from the
  sender-known-in-LRB filter in `core/core_modules/txinput_queue` (a fresh miner's holder
  ID is not yet on the ledger; the mineLock structure gates the tx). Commit `b0f76af1`.
- **Official miner `proxi node mine`** — the in-tree, wasm-wallet-style mining tool
  (`proxi/node_cmd/mine.go`). Loop: fetch the mine chain UTXO, compose the transition
  (successor + sig-locked payout + tag-along) with the txbuildercore helpers, search a
  proof-of-signing-work nonce, submit and track inclusion, repeat against the advanced
  chain. Difficulty is adaptive by default (target the current ledger slot → lowest
  available K; `--pace` forces a fixed M); a `--retarget` interval re-fetches and
  re-adapts if a target isn't solved. The hot loop is the draft PoC's template-offset
  engine (`draft/proxi-mine`), now fed real outputs and self-checked byte-identical to
  the canonical TxBuilder at startup. Mine constants A/E/P are exposed wallet-side via
  `txbuildercore.Constants` + `/ledger_constants`. UX: a bootstrap-miner banner, a live
  attempts/hashrate progress line, per-transit confirmation tracking and a run-wide
  totals line (transits, minted, held balance/UTXOs, consolidations/delegations,
  attempts, uptime). Post-confirmation `--mode`: `consolidate` (default — sweep all
  payout UTXOs into one sigLock after each success), `delegate` (every `--per` C
  confirmed transits, delegate the accumulated balance to a random alive sequencer at
  a 900-promille cut), or `stash` (leave payouts). The follow-up consolidation/delegation
  tx is fire-and-forget; only the mine tx is awaited (the next transit builds on the
  confirmed chain output). Tests: `helpers_mine_test.go` (byte-identity) and
  `ledger/tests/mine_wallet_test.go` (full wallet-build path through `utxodb`).
- **Supply reframe (InitialSupply = 10^14)** — new immutable `constTargetBaseSupply` = T =
  10^15; `constInitialSupply` = T/10 = 10^14 (genesis mints one tenth, R_init mints the
  rest). Supply-relative policy re-anchored to T (invariant to the genesis/mining split):
  `minimumInflatableAmount0 = T / SlotInflationBase` (unchanged 30303030) and
  `proformaSupplyUpperBound` use `constTargetBaseSupply`. `TargetBaseSupply` plumbed
  through `txbuildercore.Constants`; healthy-coverage still uses actual `TotalSupply`
  (scale-invariant), coverage bounds use the target-anchored proforma. Integration tests
  recalibrated to the 10x-smaller genesis. Commit `7e543511`.

Deviations from the plan, and the deferred economic calibration:
- **C is a fixed 50M** (`GenesisMineChainDust`), carved out of the bootstrap chain
  output (total genesis supply stays `constInitialSupply`). 50M safely exceeds
  `storageDeposit(256)` ≈ 44M and the mine output stays well under 256 B for life.
  Existing tests that derived the controller balance from `Supply − Faucet` now
  subtract `GenesisMineChainDust`.
- **Supply now matches the spec** (I=10^14 / T=10^15) after the reframe above; the
  earlier `constInitialSupply=10^15` / temporary ceiling `10^15 + 9e14` is gone.
- **EasyFL comparison ops** (`lessThan`/`lessOrEqualThan`) require equal-length
  operands; the constraint widens with `uint8Bytes` before comparing.
- **Adaptive retarget** was `adjust()=0` (B carried unchanged) in the first cut; it is now
  implemented — see §7.

---

## 7. Next implementation — constant K, pace 3, adaptive retarget (design, 2026-07-15)

Direction agreed after the first standalone mining run (mining works; this is a
simplification + retarget pass). **IMPLEMENTED** — this section is the design of
record; §7.5 records the chosen values. Breaking ledger change.

### 7.1 Drop the difficulty dependence on the step M

`K(M) = max(B − (M − P), E)` → **`K = B`**. Difficulty is the same no matter how long
the transition step is; `_mineK` disappears and `_minePaceAndPoW` tests the PoW against
the carried `B` directly.

Rationale: the M-dependence is a strategy dimension (wait longer → cheaper difficulty)
that miners must model and switch between. Removing it collapses the strategy to "mine
at the shortest allowed step", which simplifies miner code and — more importantly — makes
the observed step length a clean signal of hashrate for the retarget below.

### 7.2 Minimum pace P = 3

`defaultMineMinPace` 1 → **3**. Step 1 is unrealistic anyway: a miner waits for LRB
confirmation of the predecessor before building on it, so the realistic pace is ~3-4
slots. With target 4 the floor is not binding at equilibrium; P is an anti-burst rail.

### 7.3 Adaptive retarget — single-gap

Target: **one transit per 4 slots** (`constMineTargetPace`).

**The measure is the SINGLE last gap** `M = txSlot − predSlot`, already computed for the
pace check (`_mineM`; `predSlot` comes free from the consumed output). No history is
carried — the lock is just `mineLock(R, B)`.

**The rule** (the constraint forces the successor's B to this):

```
M < constMineTargetPace  -> B + 1   // too fast  -> harden, clamp at ceiling C
M > constMineTargetPace  -> B − 1   // too slow  -> ease,   clamp at floor E
M == constMineTargetPace -> B       // hold
```

One bit per transit, clamped to `[E, C]`.

**Why single-gap, not a window (this was the v1 mistake).** The first cut carried a 3-slot
ring and retargeted on `S = txSlot − s3` (the last 4 gaps). That window **lags**: after a
+1, only 1 of the 4 gaps reflects the new, slower difficulty, so `S` stays low and the
controller keeps hardening for ~4 more transits. Because each bit **doubles** solve time,
that windup ran B several bits past the operating point and off an exponential cliff — on
the first live testnet it climbed 24→29 and stalled for ~20 min per transit. Reacting to
one gap removes the lag: B reflects the very last transit's pace, so it corrects
immediately. The remaining wobble is bounded to ±1 bit (see the granularity note).

**Genesis gate (must-have).** The genesis mine output is at slot 0, so the first transit's
gap is `txSlot − 0` (huge) → would ease to the floor at once. Gate:
**`if isZero(predSlot) → carry B unchanged`**. Fires exactly once (genesis → transit 1);
from transit 2 the predecessor is a real slot. (Cheaper than the old ring gate, which had
to hold for the first 4 transits until the ring filled.)

**Floor pace vs target — the target must exceed the min pace.** With `constMineMinPace = 3`
and target 4, the harden branch (`M < 4`, reachable at M=3) can fire, so difficulty has
upward pressure and settles where the mean gap is ~4. If the target were ≤ the min pace,
`M < target` would be unreachable (gaps can't be below P) → difficulty only ever eases →
collapses to the floor E → PoW becomes trivial → mining degrades to a latency race. So
keep **target (4) > min pace (3)**.

**Constants.** `constMineBaseDifficulty` is the **seed only**; `constMineFloorDifficulty`
(E) the floor; `constMineMaxDifficulty` (C) the ceiling — required, not cosmetic: the PoW
tests only the low 64 bits (`tail(blake2b(txBytes),24)`), so K≥64 has no solution and
stalls the chain. `constMineTargetPace` is the target.

**Known limitation — 2× granularity.** Trailing-zero-bit difficulty is 2× per step, so no
difficulty gives pace *exactly* 4 — the two adjacent bits straddle it (~3 and ~6). Single-
gap makes B jitter ±1 bit around that point; realized average is ~4–4.5, not a clean 4.
"Predictable" means **averages ~4**, not reliably 4. Tight control would need continuous-
threshold PoW (`lessThan(hash64, T)` instead of counting zero bits) — a bigger EasyFL
change. **Decision: keep bits, accept the ±1 wobble** (bounded now that the lag is gone).

### 7.4 The ledger-time basis is NOT trustless — what we rely on instead

This is the important part; do not re-derive it optimistically.

**Drift is unbounded.** A mining tx is a *non-sequencer* tx: it has no baseline of its
own, and is validated inside the past cone of whichever sequencer pulls it in via the
tag-along, against *that sequencer's* baseline. The unspent mine output is in that state
regardless of how old it is, and nothing in the ledger prefers a younger timestamp. So
`txSlot` may sit arbitrarily far behind wall clock; the only floor is `predSlot + P`.

Two consequences:
- **P bounds ledger-time gaps only, not wall-clock emission.** A miner with real hashrate
  could stamp 3, 6, 9, … and mint as fast as they can compute. P is *not* an emission rail.
- **The controller regulates ledger-time emission, not wall-clock emission.** There is no
  fix inside the constraint: an EasyFL constraint sees only the tx and its inputs, so
  `txSlot` is the only clock in scope. No oracle exists.

**What actually holds it together is the incentive gradient, and it opposes the attack.**
A miner's own difficulty and reward are already fixed by the predecessor; their stamp only
affects the *successor's* difficulty, and a **bigger gap → lower difficulty**. So stamping
as late as allowed (≈ wall clock, capped by the existing future-timestamp bound and the
clock-alignment wait) is **free and weakly dominant**. Backdating is pure grief: it raises
difficulty for everyone including the griefer.

**The grief does not "keep competition at bay".** PoW races are memoryless:
`P(miner i wins a transit) = h_i / Σh`, **independent of difficulty**. Ratcheting
difficulty up does not shift shares — it slows everyone proportionally, attacker included.
The residual effect is behavioral, not mathematical: at extreme difficulty a hobbyist's
time-to-first-win stretches to impractical and they quit. Worth respecting, but it costs
the griefer real hashrate for no gain.

**The white-hat valve works, and is stronger than it looks.** An honest miner stamping
true wall clock:
1. **re-anchors** — mine-chain ledger time jumps to now, *all* accumulated drift erased in
   one transit, and backdating can only re-accumulate from the new anchor (you can never
   stamp below your predecessor); and
2. **lingers** — the huge honest gap stays in the 4-gap window for 4 transits → 4
   consecutive decreases, versus the griefer's +1 per transit they win.

So a white hat with even ~20% hashrate wins the tug-of-war.

**Preferred answer: don't depend on altruism — make it the default.** The reference miner
**stamps wall clock**. Then honest stamping is both the rational choice and the shipped
behaviour, and a griefer must fork the miner in order to hurt themselves. Same shape as
Bitcoin's mempool-policy-plus-default-client answer.

**Optional hardening.** A node-level freshness check for mining txs in `txinput_queue`
(reject stamps more than K slots behind wall clock), where `IsMiningTransaction()` is
already special-cased for the sender-known-in-LRB exemption. It is **soft**: a colluding
*sequencer* can self-include an old mine tx and other nodes will pull it during
solidification, and pulled/wanted txs bypass the input-queue filters. Still raises the bar
from "run a miner" to "own a sequencer".

**The ceiling therefore has two jobs**: the 64-bit wall, and the backstop against the
ratchet. That cuts both ways — a low ceiling bounds grief damage but also caps genuine
adaptation (if real hashrate outgrows it, difficulty saturates and emission accelerates).

### 7.5 Chosen values

| Constant | Value | Note |
|----------|-------|------|
| `constMineAmount` (A) | 1000 PROX | was 500. Emission is A/M̄ motes per slot, so A tracks the target pace; 1000 @ pace 4 restores the originally-intended 2.5e8 motes/slot (research §10) |
| `constMineMinPace` (P) | 3 | was 1; step 1 is unrealistic given the LRB-confirmation wait |
| `constMineTargetPace` | 4 | target slots per transit; single-gap retarget hardens below it, eases above (§7.3). Must stay > P |
| `constMineReliefPace` | 32 | new (§7.7). Stuck-chain relief threshold: past this gap the required K drops one bit/slot to the floor, so the chain can't wedge on difficulty. 8× target, so it never fires in normal operation |
| `constMineBaseDifficulty` (B0) | 24 | seed only now, not a max |
| `constMineFloorDifficulty` (E) | 10 | was 22 (≈4M attempts) — far too high for a genesis-era network of one or two machines: the retarget would want to go below it and couldn't, so transits would run far slower than target until hashrate arrived |
| `constMineMaxDifficulty` (C) | 40 | new. High rather than low: the grief ratchet (§7.4) costs the griefer hashrate for no gain, so headroom for real hashrate growth is worth more than bounding it. Well under the 64-bit PoW wall |

Test ledger (`ledger/tests/init.go`): B0=8 in a narrow band [6,10] with P=2 and target
pace 4, so both clamps are reachable within ~4 transits (the retarget engages from transit
2, after the genesis gate); `R_init = 8A` keeps the exhausted-chain path reachable in a
short loop.

### 7.6 What shipped

- `def/lock_mine.easyfl`: `_mineK` deleted, PoW tests `B` directly; the lock shrank from
  `mineLock(R,B,s3,s2,s1)` to **`mineLock(R,B)`** (ring dropped). `_mineSuccessorState`
  requires `B_succ == _mineAdjustedB(B)`; `_mineAdjustedB` reacts to the single last gap
  `_mineM = txSlot − predSlot`, with the `isZero(_minePredSlot)` genesis gate and the
  floor/ceiling clamps `_mineHarder`/`_mineEasier`. (The first cut used a 4-gap window
  `S = txSlot − s3`; it lagged and let B ratchet several bits past target — see §7.3.)
- `def/def_constants0.json` + `def_constants0.go` + `lib_singleton.go`:
  `constMineMaxDifficulty` and `constMineTargetPace` added; `WithMineDifficulty` gained the
  max arg and `WithMineTargetPace` was added; values per §7.5.
- **One retarget implementation, shared**: `Constants.MineAdjustedB(predB, predSlot, succSlot)`
  in `txbuildercore/helpers_mine.go` mirrors the EasyFL. `txbuildercore.Constants` is
  embedded in `ledger.Library`, so the miner (`glb.GetLedgerConstants()`) and the ledger
  tests (`ledger.L(0)`) call the same function. Go↔EasyFL agreement is not asserted directly
  (the private `_mineAdjustedB` needs a tx context); it is covered by the retarget tests,
  which build the successor with the Go helper and let the constraint validate it — a
  divergence surfaces as `wrong difficulty on mine successor`.
- Tests (`ledger/tests/mine_test.go`): K-drops-with-pace deleted, replaced by
  `TestMineDifficultySameAtAnyPace`; retarget tests cover holds-first-transit (genesis
  gate), wrong-successor-B rejected, harden-when-fast, ease-when-slow, hold-at-target, and
  both clamps.
- Miner (`proxi/node_cmd/mine.go`): K = B; stamps wall clock (never below `predSlot+P`);
  computes the successor's B via the shared helper; `--pace` (the step-choice/backdating
  lever) removed and `--retarget` renamed `--refetch`, which is what it always was — the
  interval at which the tip is re-fetched and the target re-stamped.
- **`--refetch` is adaptive by default** (`0` = adaptive; a positive value pins it).
  Re-fetching is statistically free — every attempt is an independent 2^-K trial, so
  abandoning a search and re-stamping loses no expected work — but a window far shorter
  than the solve time still churns the log and the API. The window is
  `2 * 2^K / hashrate` (the solve time is exponential, so 2x lands ~86% of targets inside
  one window), clamped to [5s, 2min], with the hashrate measured per round and smoothed
  (EWMA). The clamp is applied in float seconds: at a high K the raw window would overflow
  `time.Duration`. Observed on the standalone node: a fixed 10s window at K=24 needed
  16-34 rounds per transit; adaptive solves in 1.

### 7.7 Stuck-chain relief valve + difficulty-aware stall timeout (2026-07-23)

The single-gap retarget only eases B *after a transit lands*. But on the first live
multi-miner testnet, B climbed from the seed to a level whose solve time far exceeded the
miner's fixed 90s speculative-discard timeout — so miners abandoned every attempt
mid-solve, no transit ever confirmed, B could never ease, and the chain wedged. Two fixes,
ledger + miner, so it cannot wedge on difficulty under any conditions:

**(b) Ledger relief valve — `constMineReliefPace` (32, breaking).** The *required* difficulty
is no longer flat `K = B`. `_mineRequiredK(B, M)` is B for `M ≤ constMineReliefPace`, then
drops **one bit per extra slot** down to the floor E. So however far B overshoots the
network's hashrate, waiting long enough always makes a transit solvable — the chain can't
stay stuck. The retarget then **snaps B down** to the solved level (`_mineAdjustedB` returns
`_mineRequiredK` when `M > reliefPace`), so recovery from an overshoot is one transit, not
one-bit-per-transit. It is not gameable: the relief zone (M > 32 = 8× target) is never
reached in normal operation, because someone always solves at K=B within ~4 slots; a miner
who waits into it only delays its own reward and helps everyone equally. Liveness bound:
worst-case stuck ≈ `reliefPace + (C − E)` ≈ 62 slots (~10 min) before B snaps down.

**(a) Miner difficulty-aware stall timeout.** `mineConfirmationStall` (90s) became the
*floor* of `stallTimeout() = clamp(mineStallSolveFactor · 2^K / hashrate, 90s, 10min)`,
using the miner's measured K and hashrate (same idea as the adaptive `--refetch` window,
same float-seconds overflow guard). A legitimately slow high-K transit is no longer
abandoned mid-solve. With the relief valve, K itself drops as a stuck miner re-stamps to
later slots, so the stall timeout shrinks with the (now achievable) difficulty — the two
fixes reinforce each other.

Shipped: `_mineRequiredK` in `def/lock_mine.easyfl` (used by both `_minePaceAndPoW` and the
retarget snap-down); `constMineReliefPace` through the constants + `txbuildercore.Constants`;
shared `Constants.MineRequiredK`; miner mines to `MineRequiredK` and the stream verifier
checks against it; `miner.stallTimeout()`. Tests: on-chain `TestMineReliefValveLowersRequiredK`
plus exhaustive `TestMineRequiredK` / `TestMineAdjustedBReliefSnapDown` unit tests.

---

## 8. Pace-relief difficulty (K(M) + retarget)

Status: **IMPLEMENTED** (2026-08-03), not yet deployed to the live testnet. Supersedes
§7.3's flat-`K = B` + stuck-chain relief valve. Breaking ledger change (LibraryHash +
genesis) → coordinated redeploy. Assessed against the observed behaviour of §7 on the live
testnet; expected to stabilize the pace at the target and remove the sawtooth.

**What shipped** (all in one commit on `develop`):
- `def/lock_mine.easyfl`: `_mineRequiredK` re-anchored `constMineReliefPace → constMineMinPace`
  and made always-on (`K = max(B − (M − P), E)`); `_mineAdjustedB` lost its relief snap-down
  branch (now pure genesis-gate + ±1 harden/hold/ease). `constMineReliefPace` deleted from
  `def_constants0.json/.go`, `ledger.Constants` (extraction + display line), and
  `txbuildercore.Constants` (field + JSON + marshal/unmarshal).
- Go mirrors in `txbuildercore/helpers_mine.go`: `MineRequiredK` anchored at `MineMinPace`;
  `MineAdjustedB` dropped the `gap > reliefPace` snap-down case.
- Miner (`proxi/node_cmd`): the target-slot walk was already `predSlot + P` walking forward
  with the clock, so only the fork-choice changed — `mine_tree.go betterThan` now breaks ties
  on the **oldest (smallest) `txSlot`** (heaviest under the pace-relieved K) instead of the
  trailing-zero count; `mineTreeNode.txSlot` replaces `powZeros`; `powZeroBits` deleted. Only
  comments changed in `mine.go` (K formula, stall timeout, `successorSlot`).
- Tests: `mine_test.go` gained `mineExactK` (deterministic below-required-K PoW) and replaced
  the flat-K tests with `TestMinePaceRequiresFullBAtMinimum` / `TestMinePaceRelievesRequiredK`
  / `TestMineHugePaceLandsAtFloorK`; `helpers_mine_test.go` reworked `TestMineRequiredK` and
  replaced `TestMineAdjustedBReliefSnapDown` with `TestMineAdjustedB`; `mine_tree_test.go`
  tie-break tests reworked for `txSlot` (helpers lost the `powZeros` arg, gained an optional
  slot). `go test ./ledger/... ./proxi/node_cmd/...` green.

The design below is the spec of record; it matches what shipped.

### 8.1 What §7 got wrong (the sawtooth)

With `K = B`, the pace is a **step function** of B: while B is below the solvable level the
miners are pinned at the min pace (they solve faster than P slots), so every gap is `M = P`
→ `M < target` → harden **every transit**, with no feedback, until one bit tips solve-time
past the pace floor and the pace jumps ~2×. A one-bit change in B swings the pace 3↔6, so
the retarget can never land on the target — it overshoots and oscillates. The relief valve
(§7.7) only backstops the tail into a *recoverable* sawtooth; it doesn't stop the climb.

### 8.2 The fix — restore the pace term in the required difficulty

**This is literally the §7.7 relief formula re-anchored from `constMineReliefPace` (32) to
`constMineMinPace` (3) and made always-on.** It is the old `K(M)` the first cut had (§7.1
deleted it); the deletion was the cause of the sawtooth.

**Required difficulty (constraint + Go mirror):**
```
K_required(B, M) = max(B − (M − P), E)
    P = constMineMinPace, M = txSlot − predSlot, E = constMineFloorDifficulty
```
- `M = P` → `K = B` (heaviest). Each extra slot of pace → one bit easier. Floored at E.
- Since the pace check already enforces `M ≥ P`, this is well-defined for every valid transit
  and needs the same underflow-safe clamp the current `_mineRequiredK` uses (if `M − P ≥
  B − E` return E, else `B − (M − P)`).
- **Subsumes the relief valve**: `K → E` as `M` grows, so the chain can never stick regardless
  of how far B sits above the network's capacity. Delete `constMineReliefPace` entirely.

**Retarget — unchanged from §7.3 (single-gap ±1 on the base B):**
```
M < constMineTargetPace  → min(B+1, C)   [harden]   (P=3, target=4: fires at M=3)
M == constMineTargetPace → B             [hold]
M > constMineTargetPace  → max(B−1, E)   [ease]
```
- Genesis gate unchanged: `isZero(predSlot) → hold B` (first transit's M is huge → K = E).
- **Drop the `M > reliefPace → snap-down` branch** in `_mineAdjustedB` — no longer needed
  (K(M) provides liveness; the ±1 ease provides gradual recovery from a hashrate crash, and
  with K(M) there is no large overshoot to recover from in the first place). Note snapping to
  the solved K is *wrong* at `M = P` (it would hold instead of harden), which is why the
  retarget stays ±1.

### 8.3 Why it stabilizes at the target pace

`K(M)` **linearizes** pace-vs-B: it spreads the exponential across the *time* axis, so the
winning pace becomes `M ≈ B − log₂(H·slot) + P` — a smooth **~1-slot-per-bit** function of B
instead of a 2× step. A ±1 jitter in B now moves the pace ±1 *slot* (self-correcting next
transit) instead of 3↔6. The retarget drives B to where the winning pace = target and
**holds** it (target → no change). Equilibrium `B ≈ log₂(H·slot) + (target − P + 1)`; e.g. at
~220k H/s combined and target 4, `B ≈ 22`, pace ≈ 4, ±1-slot jitter — *lower and stable* vs
§7 overshooting to 27 and sticking. Emission becomes a clean `A` per `target` slots (matches
how A was sized in §10). This is continuous-difficulty regulation achieved through the pace
axis instead of 64-bit-threshold PoW — cheaper, and it reuses the bit-count PoW.

### 8.4 The txSlot-dependence is not gameable (why §7.1's removal reason doesn't hold)

`K(M)` makes the required difficulty depend on the miner's chosen slot. In the competitive
race this is self-regulating, not a lever: to stamp a later (easier) slot you must actually
wait for the wall clock (the node holds future-stamped txs), and while you wait everyone's
required K ramps down together — the *first* miner to solve at *any* pace wins, so submitting
ASAP dominates and delaying only risks losing the transit. The retarget isn't gameable
either (early = harden for everyone = self-harming; late = ease for everyone = no private
gain). §7.1's claim that `K = B` gives "a clean hashrate signal" was wrong — pace pins at the
floor and B ratchets; `K(M)` is what makes the pace a real signal.

### 8.5 Miner (proxi/node_cmd)

- **Mine to `MineRequiredK(B, M)`** — formula change only (the miner already computes K via
  `MineRequiredK`).
- **Target-slot walk**: target the oldest allowed slot (`predSlot + P`, highest K = heaviest)
  first; as the wall clock advances without a solution, re-stamp forward (K drops one bit per
  slot) and submit the first solution found. The existing re-stamp / adaptive-`--refetch`
  loop already walks the slot forward with the clock; ensure it starts at `predSlot + P`.
- **Fork-choice (`mine_tree.go` `betterThan`) — change the tie-break to match "heaviest":**
  1. height = chain transition counter (longest chain) — *unchanged*, primary.
  2. **oldest (smallest) txSlot** — at a given height a smaller txSlot required a higher
     `K = B − (M − P)`, so it is the heaviest transit. **Replaces the `powZeros` comparison.**
  3. biggest tag-along fee — the branch a sequencer is likelier to confirm. *Keep.*
  4. lowest txid — determinism. *Keep.*

  Rationale (user's framing): preferring the oldest txSlot is "roughly equivalent to switching
  to the heaviest difficulty", and unlike raw trailing-zero count it is **non-grindable** — to
  claim an older slot you must meet the higher K the constraint requires there. All honest
  miners then converge on the heaviest branch. `mineTreeNode` gains a `txSlot` field (the
  transit's successor slot, `tip.oid.Timestamp().Slot`); `powZeros` drops out of the tie-break.
- The stream verifier already checks against `MineRequiredK`; only the formula changes.

### 8.6 Constants

- **Remove** `constMineReliefPace` and `MineReliefPace` everywhere (`def_constants0.json/.go`,
  `ledger.Constants`, `txbuildercore.Constants` + its JSON, `WithMine*`). Consider seeding a
  **lower `constMineBaseDifficulty`** for a small testnet so B eases up gently.
- Keep seed / floor (E) / ceiling (C) / min pace (P) / target pace.

### 8.7 Tests

- Ledger (`ledger/tests/mine_test.go`): a transit at pace > P is accepted at the relieved K
  and rejected if it only meets an even-lower K; the pace-P transit must meet full B; retarget
  harden/hold/ease unchanged; huge-M transit lands at floor K (liveness); genesis gate holds B.
- Go unit (`helpers_mine_test.go`): `MineRequiredK(B, M)` table (K = B at M=P, −1/slot, floor
  clamp); `MineAdjustedB` harden/hold/ease with the snap-down branch removed.
- Miner (`mine_tree_test.go`): `betterThan` tie-break prefers the oldest txSlot, then the
  bigger fee; equal-height/equal-slot falls to fee then txid. Rework the existing
  `TestMineTreeTieBreaksOn*` / `WorkDominatesFee` tests for txSlot instead of `powZeros`.

### 8.8 Migration note

Most of the ledger diff is small: `_mineRequiredK` swaps `constMineReliefPace → constMineMinPace`
and loses the outer "flat B below the threshold" branch; `_mineAdjustedB` loses its relief
branch; the reliefPace constant is deleted. The visible work is the miner fork-choice
(txSlot tie-break) and reworking the tie-break tests. Breaking (LibraryHash + genesis) →
coordinated redeploy.
