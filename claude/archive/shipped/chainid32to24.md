# ChainID 32 → 24 bytes: cost/benefit analysis

Status: analysis only (not implemented). develop08, 2026-06-01.

## Question

Should `ChainID` be reduced from 32 bytes to 24 bytes (192 bits)?

## What ChainID is

`ChainID = blake2b256(originOutputID)` — currently a **full 32-byte** hash.

- Definition: `ledger/base/id.go:21` (`ChainIDLength = 32`), type `ChainID [32]byte`.
- Derivation: `ledger/base/id.go:472` `MakeOriginChainID(oid) = blake2b.Sum256(oid[:])`.
- It is **not** a transaction ID (32B) or output ID (33B); it is a derived hash, so
  its length is **independent** of those. The fact that it is currently also 32B is
  incidental — decoupling it to 24 is clean.
- Origin chains use `NilChainID` (all-zeros); the successor's real ChainID is computed
  inside the chain constraint as `blake2b(inputIDByIndex(selfOutputIndex))`
  (`ledger/def/chain.easyfl:104`).

## Key reframing: ChainID is NOT a niche field

The chainID is repeated across the **dominant, abundant** state classes, in
**multiple places per UTXO**:

1. **Chained accounts are first-class** and expected to be as abundant as
   accounts/addresses on other UTXO chains — potentially **millions of delegations**.
   Each delegation is a chain output carrying a chainID.
2. **A native-token tag IS the foundry's chainID** (the foundry is identified by the
   chain it lives on; `native_token.easyfl` header). So the chainID is repeated with
   **every native-token ownership UTXO** on the ledger — the workhorse UTXO class of a
   token economy. The tag is not a separate hash-width field; it tracks the chain-ID
   width 1:1. Shrink the chain ID → the tag shrinks with it automatically.

This is exactly the case the project rule was written for: minimise per-UTXO bytes even
at per-tx cost, because UTXOs persist longer than the tx that creates them
(`feedback_utxo_vs_tx_bytes`).

## Where the chainID physically lands (per-site byte accounting)

Saving per occurrence = **8 bytes** (32→24, −25%). The gap between "8B" and "24B"
intuitions is *what you count as "in the UTXO"* — the chainID is stored in more than
one physical place.

### Native-token UTXO (normal user holding token T, sig-locked) — the heaviest case

| Site | Saved | Where it lives | Code |
|------|-------|----------------|------|
| `tokenAmount(tag, amount)` inline literal | 8B | output leaf (constraint slot 3/4) | `native_token.go:161,305` |
| slot-1 compound entry `controller \|\| tag` (64B) | 8B | output leaf (index-value tuple, slot 1) | `native_token.go:326-336` (`WithTokenAmount` → `addCompoundIndexValue`) |
| controllers trie key `partition \|\| controller \|\| tag` | 8B | trie index (separate row) | `mutate.go` indexer, iterates slot 1 |

- **Output-leaf bytes only: 16B** — the tag is written **twice** in the serialized
  output (once as the `tokenAmount` arg, once inside the slot-1 compound entry that
  enables `holderID || tag` prefix-iteration of "my UTXOs of token T").
- **+ controllers trie key: 8B** → **24B total system-wide.**
- The "8B/token" intuition counts only the `tokenAmount` literal and misses the slot-1
  duplicate and the index row.

### Delegation chain UTXO

| Site | Saved | Where it lives | Code |
|------|-------|----------------|------|
| chain constraint arg `$0` (delegation's own chainID) | 8B | output leaf (slot 3) | `chain.easyfl:181`, `chain.go:43,113` |
| chain-tip trie key `TriePartitionChainID \|\| chainID` | 8B | trie index | `mutate.go:519,535` |

- **Output-leaf bytes only: 8B.** **+ trie key 8B → 16B total.**

### Other chain outputs (sequencer, foundry)

Same as delegation's own-chain cost: chain constraint arg (8B leaf) + chain-tip trie key
(8B). Plus chain-locked outputs (tag-along) carry the target chainID in the slot-1
index-value tuple (8B leaf) + a controllers trie row (8B) — but these are short-lived
(consumed quickly by the sequencer), so they count more as tx-bytes than persistent
state. The foundry constraint stores **supply only**, not the tag — the tag is read off
the sibling chain constraint — so there is no extra foundry-side tag copy.

### Reconciliation of the two accountings

| Class | Output leaf only | Full footprint (leaf + trie keys) |
|-------|------------------|-----------------------------------|
| Native-token UTXO | **16B** (two copies) | **24B** |
| Delegation UTXO | **8B** (one copy) | **16B** |

Native-token UTXOs are the **heavier** case, not the lighter one. Whichever accounting
is used, token-UTXO saving ≥ delegation saving, and both scale with the most abundant
state classes.

## Magnitude at scale (order of magnitude)

For a mature ledger, e.g. 10M native-token UTXOs + 2M delegations:

- token UTXOs: 24B × 10M ≈ **240 MB**
- delegations: 16B × 2M ≈ **32 MB**
- → **~270 MB of permanent state**, plus smaller trie keys → better cache locality,
  smaller Merkle proofs, faster sync.

The saving rides on the bulk of long-lived state, 2–3× per UTXO — not a niche.

## Costs

- **Consensus-breaking hardfork.** The chainID width lives in EasyFL constraint
  bytecode, so this is a ledger rule change, not a code-internal refactor.
- **blake2b truncation must be added in two languages, consistently:**
  - Go: `MakeOriginChainID` (`id.go:472`) → `blake2b.Sum256(...)[:24]`.
  - EasyFL: `chain.easyfl:104` `blake2b(inputIDByIndex(selfOutputIndex))` and
    `lock_chain.easyfl` `_selfReferencedChainIDAdjusted`'s `blake2b(inputIDByIndex($0))`
    must wrap in `slice(...,0,23)` to truncate to 24.
  - Length check `chain.easyfl:181` (`u64/32`) → `u64/24`; origin zero-literal
    `chain.easyfl:2` (32-byte literal) → 24-byte.
- **Trie-key length disambiguation must change.** Today single-controller = 32B,
  compound = 64B (`feedback_indexing_via_slot1`). With a 24-byte tag the compound becomes
  32 (controller, a blake2b lock hash — stays 32) + 24 (tag) = **56B**. Still unambiguous
  vs 32, but the documented constant and any length-switch on it must be updated.
- **Regenerate** `BoostrapSequencerIDHex` (`ledger/base/genesis.go`, `ledger/genesis.go`)
  and re-validate; regenerate test fixtures and hardcoded 64-hex chainIDs
  (e.g. `chain.go:196` uses `blake2b.Sum256("dummy")` directly as a ChainID).
- **Breadth:** ~173 files reference ChainID, but most recompile cleanly via the
  `ChainIDLength` constant / the typed array. Concrete hand-edits concentrate in ~5–6
  files + EasyFL + fixtures.

## Security at 24B (192 bits)

Safe, including for the token-tag role:

- ChainID = blake2b(originOutputID). Forging a **specific** existing chain/token's id is
  a **second-preimage** = 192 bits → infeasible.
- A **birthday collision** (96-bit work) only finds two foundries/chains the attacker
  already controls → no economic advantage (can't forge another holder's token).
- Precedent: `TransactionHash` is already a truncated blake2b (26 bytes / 208 bits)
  (`id.go:17`). 24 bytes is a sound floor.

## Verdict

**Worth doing**, given chained accounts and native tokens as first-class, abundant
citizens. It trims a field that rides on the majority of permanent state, 2–3× per UTXO,
for a fixed one-time hardfork cost; 192-bit security stays safe. (An earlier
"niche → skip" reading was conditioned on chainID being rare, which it is not under this
design.)

Riders / open items to confirm before implementing:

- Confirm EasyFL inline-data encoding yields a clean 8B delta at 24 vs 32 (no
  length-prefix bracket shift around the data-size threshold).
- Update the `feedback_indexing_via_slot1` disambiguation note: compound entry 64 → 56.
- Verify nothing treats ChainID and TransactionID as interchangeable lengths (none found
  in the survey, but grep before implementing).
- Best bundled with another planned ledger break to amortise the genesis-constant /
  fixture / bytecode regeneration, though it now stands on its own merits.
- Orthogonal (not part of 32→24): foundry-tag **interning** (store a short per-state
  local id instead of the full chainID in token UTXOs) would attack the same cost on a
  different axis; bigger redesign, out of scope here.
