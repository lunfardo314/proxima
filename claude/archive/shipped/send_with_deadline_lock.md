# sendWithDeadline lock — spec

## 0. Purpose

`sendWithDeadline` is a lock for **conditional, deadlined transfers**.
The master commits tokens to a target (either a sigLock holder or a
chainLock chain ID) for a bounded window: if the target spends within
the acceptance window the transfer completes; otherwise the master
reclaims; otherwise (after a public-cleanup boundary) anyone can
purge the dust.

A strict generalisation of `tagAlong`:

| feature              | tagAlong                       | sendWithDeadline                                 |
|----------------------|--------------------------------|--------------------------------------------------|
| target kind          | sequencer chain only           | sigLock holder OR chainLock chain (per output)   |
| acceptance window    | hardcoded (~5 min)             | per-output, uint32 slot count, floor 30          |
| master-reclaim window| hardcoded (~1 h)               | per-output, uint32 slot count, floor 1000        |
| public cleanup       | after the reclaim window ends  | after the reclaim window ends                    |
| sender restriction   | tx signer == master at produce | same                                             |

`tagAlong` remains the specialised low-fee transfer-to-sequencer lock;
`sendWithDeadline` handles the general case (escrow-style transfers,
conditional payments to chains, etc.) at the cost of ~10 extra bytes
of lock bytecode per UTXO.

## 1. Windows

Let `Δ = txSlot − createSlot` where `createSlot` is the slot of the
UTXO's outputID timestamp.

```
        target                  master                    public
       acceptance               reclaim                   cleanup
   |---- ... ----|------------ ... ------------|------------ ... ------------→  Δ
   0       acceptanceSlots               cleanupSlots
```

- `0 ≤ Δ < acceptanceSlots`         → **target** can spend.
- `acceptanceSlots ≤ Δ < cleanupSlots` → **master** can spend (reclaim).
- `cleanupSlots ≤ Δ`                  → **anyone** can spend (public cleanup).

Produce-time floors enforced by the constraint:

- `acceptanceSlots ≥ 30`                        — target has ≥ 30 slots
- `cleanupSlots − acceptanceSlots ≥ 1000`       — master has ≥ 1000 slots
- `cleanupSlots > acceptanceSlots` is implied by the line above.

Wallet defaults: `acceptanceSlots = 60`, `cleanupSlots = 8000` (master
reclaim window = 7940 slots ≈ 22 minutes at 10 slots/s).

## 2. Wire form

```
output[0]   amounts              standard
output[1]   indexValues = [masterID, targetID]   both 32 B, both indexed
output[2]   sendWithDeadline(targetType, acceptanceSlots, cleanupSlots)
output[3..] free for extra constraints (tag-along, etc.)
```

### 2.1 Index values (slot 1)

- Position 0 = `masterID` — sender's 32-byte holderID (master-first
  §4.1 convention, matching tagAlong / htlc / delegateLock).
- Position 1 = `targetID` — 32 bytes:
    - if `targetType == 0x00`: a sigLock **holderID**.
    - if `targetType == 0x01`: a **chainID**.

Both values are indexable; both parties can find their pending
sendWithDeadline outputs via the standard `get_outputs` indexer query.

### 2.2 Lock bytecode (slot 2)

`sendWithDeadline(targetType, acceptanceSlots, cleanupSlots)` — public
3-arg constraint:

- `$0` `targetType` — 1 byte. `0x00` = sigLock target, `0x01` = chainLock target.
- `$1` `acceptanceSlots` — 4 bytes BE uint32. Must be ≥ 30 at produce time.
- `$2` `cleanupSlots`    — 4 bytes BE uint32. Must satisfy
  `$2 − $1 ≥ 1000` at produce time.

Reconstruction uses `LockFromOutputElementsWithLib`, which dispatches
on the bytecode prefix and reads the three args via
`ParseBytecodeOneLevel`.

## 3. Validation

### 3.1 Produced-side

The producer of a `sendWithDeadline` output must satisfy ALL of:

- `selfBlockIndex == lockConstraintIndex`
- `len(masterID) == 32` and `masterID != 0`
- `len(targetID) == 32` and `targetID != 0`
- `targetType ∈ {0x00, 0x01}`
- `acceptanceSlots ≥ 30`
- `cleanupSlots ≥ acceptanceSlots + 1000`
- `masterID == txHolderID(txSignatureData)`        — sender == tx signer
- `selfNumConstraints < 6`                         — keeps UTXO small
- `selfEnforceZeroAmountsInNonChainedOutput`       — only the token-balance slot of amounts carries tokens

### 3.2 Consumed-side

Let `Δ = txSlot − createSlot`. The consumer satisfies exactly ONE of:

1. **Public cleanup**: `Δ ≥ cleanupSlots`. No unlock check; anyone can spend.
2. **Master reclaim**: `acceptanceSlots ≤ Δ < cleanupSlots` AND
   `_sigLock(masterID)` validates.
3. **Target accept**: `Δ < acceptanceSlots` AND:
    - `targetType == 0x00`: `_sigLock(targetID)` validates.
    - `targetType == 0x01`: `_chainLock(targetID)` validates.

Outside these three windows / unlock conditions the constraint fails.

## 4. EasyFL error labels

Produced:
- `locks_must_be_at_lockConstraintIndex`
- `sendWithDeadline:_32-byte_targetID_expected`
- `sendWithDeadline:_32-byte_masterID_expected`
- `sendWithDeadline:_non_zero_targetID_expected`
- `sendWithDeadline:_non_zero_masterID_expected`
- `sendWithDeadline:_targetType_must_be_0x00_or_0x01`
- `sendWithDeadline:_acceptanceSlots_below_floor`
- `sendWithDeadline:_master_reclaim_window_below_floor`
- `sendWithDeadline:_master_hash_check_failed`
- `sendWithDeadline:_too_many_UTXO_elements`

Consumed:
- `sendWithDeadline:_acceptance_window:_target_unlock_failed`
- `sendWithDeadline:_reclaim_window:_master_unlock_failed`

(Public-cleanup branch has no unlock check.)

## 5. Go API

Mirrors `TagAlongLock` shape:

```go
const SendWithDeadlineLockName = "sendWithDeadline"

const (
    SendWithDeadlineTargetSigLock   byte = 0x00
    SendWithDeadlineTargetChainLock byte = 0x01
)

const (
    SendWithDeadlineMinAcceptanceSlots = uint32(30)
    SendWithDeadlineMinReclaimSlots    = uint32(1000)
)

type SendWithDeadlineLock struct {
    MasterID        base.HolderID
    TargetID        base.HolderID // 32 bytes — sigLock holderID OR chainID, per TargetType
    TargetType      byte
    AcceptanceSlots uint32
    CleanupSlots    uint32
}

func (l *SendWithDeadlineLock) Name() string
func (l *SendWithDeadlineLock) Source() string       // "sendWithDeadline(u8/T, u32/A, u32/C)"
func (l *SendWithDeadlineLock) LockBytecode() []byte
func (l *SendWithDeadlineLock) IndexValues() [][]byte  // [master, target]
func (l *SendWithDeadlineLock) String() string

func NewSendWithDeadlineOutput(amount uint64, l *SendWithDeadlineLock) *Output
func SendWithDeadlineLockFromOutputElements(indexValues, lockBytecode []byte, lib *Library) (*SendWithDeadlineLock, error)
```

Registration: `registerSendWithDeadlineLock(lib)` mirrors
`registerHTLCLock` (multi-arg flavour with a `lockKindMarker`), invoked
from `def_upgrade0.go` next to the existing lock registrations.

`LockFromOutputElementsWithLib` learns a new `case SendWithDeadlineLockName:`
that calls `SendWithDeadlineLockFromOutputElements`.

## 6. Tests (`ledger/tests/send_with_deadline_test.go`)

Mirrors `lock_tag_along` coverage + per-window matrix:

- **Produce happy**: target=sigLock, then target=chainLock — both settle.
- **Produce rejects** (one each):
  - zero `masterID`; zero `targetID`;
  - `targetType` outside {0x00, 0x01} (e.g. 0x02);
  - `acceptanceSlots < 30`;
  - `cleanupSlots - acceptanceSlots < 1000`;
  - master ≠ tx signer;
  - lock at non-lock slot;
  - too many UTXO elements.
- **Consume — target accept (sigLock)**: tx signed by target's key,
  `Δ < acceptanceSlots`. Asserted success + key-mismatch rejection.
- **Consume — target accept (chainLock)**: tx consumes a chain output
  matching `targetID`; `Δ < acceptanceSlots`. Asserted success.
- **Consume — master reclaim**: `Δ ≥ acceptanceSlots`, signed by master.
  Non-master signer in the same window → rejected.
- **Consume — master reclaim too early**: master tries to spend with
  `Δ < acceptanceSlots`. Rejected.
- **Consume — public cleanup**: `Δ ≥ cleanupSlots`. Third-party signer
  succeeds.
- **Boundary tests**:
  - `Δ == acceptanceSlots` → master's window (not target's).
  - `Δ == cleanupSlots` → public's window (not master's).
- **Builder round-trip**: serialise → parse → re-serialise; byte-equal,
  field-equal.

Tests use the existing UTXODB harness with explicitly-set tx
timestamps to control `Δ`. No real-time advancement needed.

## 7. Migration / interop

New lock kind atop "arbitrary EasyFL bytecode at slot 2" — no migration
of existing outputs. Old wallets that don't know the prefix render it
as `generalLock(0x…)` via the existing unrecognised-prefix path; they
can't spend it but also don't crash.

`tagAlong` stays as the cheap sequencer-fee primitive;
`sendWithDeadline` is the general escrow.

## 8. Out of scope

- Multi-recipient / multi-sig variants.
- Partial payment (whole UTXO is spent atomically).
- Cross-tx persistence of acceptance state.
- Caps on `cleanupSlots` (sender chooses freely).
