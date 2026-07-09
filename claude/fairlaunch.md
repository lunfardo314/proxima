# Fair Launch — the `mine` chain: finalized spec & implementation plan

Status: FIRST CUT IMPLEMENTED (branch `fairlaunch`, off `develop`). Breaking hardfork.
See §6 for shipped status; remaining work is the adaptive-difficulty retarget and the
input-flood filter (§4). The official miner has shipped as the in-tree `proxi node mine`
command (§6).
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
the official `proxi node mine` miner have since shipped — see §6.)

---

## 1. Parameters (first cut)

Motes: 1 PROX = 10⁶ motes. Slot τ = 10.24 s (128 ticks × 80 ms); ≈ 3.08 M slots/yr.

| Symbol | Value | Meaning |
|---|---|---|
| I | 10¹⁴ motes (10⁸ PROX) | genesis supply (bootstrap sequencer output) |
| T | 10¹⁵ motes (10⁹ PROX) | mintable ceiling (not a held sum) |
| R_init | T − I = 9×10¹⁴ motes | initial value of the remaining-mintable counter R |
| A | **500 PROX = 5×10⁸ motes** | minted per transit |
| N | R_init / A = 1.8×10⁶ | total transits to exhaust R |
| C | fixed dust, sized to worst-case output bytes | the mine output's own balance, constant forever (see storage-deposit note) |
| B₀ | global const (base/initial difficulty) | seeds the mutable B at genesis; e.g. testnet 24, tests 8 |
| E | global const (floor difficulty, 0 < E < B) | e.g. testnet 22 |
| P | global const (min pace, slots) | 1 (production); tests use 2 so a below-minimum pace is testable |

Difficulty curve: `K(M) = max(B − (M − P), E)`, `M = succ.slot − pred.slot ≥ P`,
`K(P) = B`. Requirement `B < 64` (the PoW test operates on the low 64 hash bits).

Emission (from the model, `fairlaunch-research.md §9`): at the realistic LRB-imposed
floor pace M̄≈2, doubling of I (I/A = 2×10⁵ transits) takes ≈47 days — inside the
1–2 month target; full emission ≈1.17 yr. Pace 1: doubling ≈24 days.

The real decentralization-threshold timing is somewhat *longer* than these pure-mining
figures: I itself keeps inflating (chain/branch inflation on the bootstrap sequencer and
other chains), so the total supply miners must overtake to cross 50%/33% grows during
emission. This does **not** touch the mine constraint (A and R are fixed); if we want the
threshold to land on schedule, the lever is a modest bump to T (mint more), not any
change to `mineLock`.

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
4. **Difficulty carry + slot ring roll.** `B_succ = B_pred` (static first cut; adaptive
   retarget in §4). Ring rolls: `s1_succ = pred.slot`, `s2_succ = s1_pred`,
   `s3_succ = s2_pred`. A, E, P are global constants, not carried.
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
  mutable B. The plumbing ships in the first cut; only the retarget formula is deferred:
  - The slot ring `s1,s2,s3` (three slots preceding the predecessor) is **carried and
    rolled every transit in the first cut** (rule 4), and B is a **mutable arg**. What
    the first cut omits is only the retarget: `adjust()=0`, so `B_succ=B_pred`.
  - Turning it on is a one-function change: `B_succ = clamp(B_pred + adjust(steps), E,
    cap)` where `steps` are the recent step lengths derived from the 5 visible slots
    (`s3,s2,s1,pred.slot,succ.slot` → up to 4 steps). The difficulty for the *current*
    step must be computable from predecessor-side data only (its ring + `pred.slot`), so
    the miner knows K before mining; the successor slot only closes the step and rolls
    the ring for the next miner.
  - **Genesis seeds the ring to all-zero**; the retarget formula special-cases zero
    slots (a not-yet-full ring → skip retarget, hold B) so the first three transits
    behave. The first-cut difficulty logic ignores the ring entirely, so zero seeds are
    inert there.
  - Retarget target and clamps come from the model (research §9: hold observed pace at a
    schedule target; B must be free to track order-of-magnitude hashrate — hence (A),
    not a bounded derive-from-ring). The concrete formula is the one open design piece.

---

## 5. Decisions

Resolved:
- **Param split** — A, E, P, B₀ (+ retarget params) are global ledger constants (set like
  tick duration); mutable state (R, B, slot ring) lives in `mineLock` args. (A) stored
  mutable B; ring carried+rolled in the first cut but ignored by the static difficulty.
- **Genesis chain-ID recompute is breaking** — `BoostrapSequencerIDHex` (both copies) and
  the new `MineChainIDHex` change; accepted (this branch is a breaking hardfork).

Still open:
- **Concrete first-testnet B₀/E/P** — placeholder 24 / 22 / 1. Unit tests 8 / 4 / 1.
- **Zero-fee mine tx** — rule 7 caps T at 1% of A but sets no *minimum*; `A'=A, T=0` is
  allowed as written (miner then needs another path to a sequencer). Keep permissive
  unless a minimum tag-along is wanted.

## 6. Implementation status (branch `fairlaunch`, 2026-07-07)

Shipped, `go test ./ledger/...` green:
- **3.0 constants** — `constMineChainID`, `constMineAmount`, `constMineMinPace`,
  `constMineBaseDifficulty`, `constMineFloorDifficulty`, `constMineRemainingInit` in
  `def/def_constants0.json`; `InitParameters.Mine*` fields + defaults (A=500 PROX,
  B=24, E=22, P=1, R_init=9e14) in `def_constants0.go`; `WithMineDifficulty` test option.
- **3.1 genesis** — mine output at index 3; `GenesisTransactionIDShort` bumped 2→3;
  both chain-ID hexes recomputed (`BoostrapSequencerID=adffaebe…`,
  `MineChainID=5560bf95…`) in `base/genesis.go`, `genesis.go`, `def_constants0.json`;
  `GenesisMineChainOutput()`; wired into `multistate/genesis.go` +
  `genesis_snapshot.go` (`InsertRange(0,4)`).
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
  `txbuildercore.Constants` + `/ledger_constants`. Tests: `helpers_mine_test.go`
  (byte-identity) and `ledger/tests/mine_wallet_test.go` (full wallet-build path through
  `utxodb`).
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
- **Adaptive retarget** still `adjust()=0` (B carried unchanged); the slot ring is
  carried and rolled, ready to turn on (§4).
