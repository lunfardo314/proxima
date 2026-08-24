# Chess PoC

## 0. Goal

Demonstrate UTXO covenant programming with redeemed local scripts by
running a full chess game on the Proxima ledger. Two redeemed local
scripts compose:

- **chessValidator** — rule-pure half-move validator, lives in
  `easyfl/chess/`. Public API documented in
  `chess_script.md` §9.0 (`boardOK`, `isStart`, `sideToMove`,
  `playerMove`, `isCheck`).
- **chessGame** — the on-chain protocol layer this PoC adds. Wraps
  chessValidator via `callRedeemer`; manages turn locking, deadlines,
  bounty, tie / resign / timeout flow. Pinned to
  chessValidator's hash via `CompileLocalScriptWithCheck` at compile
  time.

Both binaries are committed via `redeemScript` in every chess tx (see
`claude/archive/shipped/local_script.md`). The chess UTXO's lock dispatches into
chessGame via `callRedeemer(chessGameHash, branch, …)`, which in turn
calls `callRedeemer(chessValidatorHash, …)` for move-rule predicates.

## 1. Workflow

A chess game is a chained UTXO. One UTXO transition = one half-move.
The lock alternates which holder ID is required to sign, driven by
`sideToMove(board)` from chessValidator.

- Anyone can start a game by creating an origin UTXO that:
  - stakes amount A;
  - records white's holder ID (= the signer of the origin tx);
  - records white's first move (e.g. `e2-e4`) applied to the canonical
    starting position;
  - sets a per-game move-time budget T (slots) and a deadline.
- Anyone can accept by being the first to consume the origin: their
  tx binds `blackHolderID`, contributes ≥ A more to the chain, and
  plays black's first move.
- After acceptance the chain proceeds in turns until termination.
- The acting player must spend the predecessor before its deadline
  (`txSlot < predecessor.deadline`). Once `txSlot ≥
  predecessor.deadline`, the *opposite* side can claim the entire
  chain — this collapses both "actual time-out" and "checkmate /
  stalemate" into the same on-chain rule (per chess_script.md §9.3,
  the script does not enumerate moves to detect mate/stalemate).
- originator can consume origin UTXO any time. The deadline becomes in effect after blacks accept the challenge 
- Side-to-move can resign at any time after acceptance (single tx
  pays full bounty to opponent).
- Side-to-move can offer a tie alongside their move (set
  `tieProposed` flag in the produced state). The opponent's next tx
  either ignores the offer (plays an ordinary `move`, which clears the
  flag) or takes the `tie-accept` branch (chain ends, bounty split
  50/50 — particular last move not relevant).

Bounty rules:
- Move 1 (origin): amount = A.
- Move 2 (acceptance): amount ≥ 2A (black contributes ≥ white's stake).
- Move 3+: amount ≥ predecessor.amount (monotone non-decreasing).
- Chain inflation accrues normally; tag-along payments are a separate
  output and remain each player's own concern.

Pre-acceptance abort: if no black accepts before `origin.deadline`,
white can use the `timeout-claim` branch to reclaim the chain.
chessGame detects the empty `blackHolderID` and admits white as
claimant.

## 2. State schema

A chess UTXO follows the standard tuple layout (see CLAUDE.md, "UTXO
tuple layout"):

| Idx | Content |
|-----|---------|
| 0 | amounts vector |
| 1 | index-values tuple — `[whiteHolderID, blackHolderID]`. Both indexed by the trie indexer so a player can find their active games via the standard get-outputs query. |
| 2 | lock = `chess()` (callRedeemer dispatch into chessGame) |
| 3 | chain constraint |
| 4 | `chessState` — tuple, see below |

`chessState` (idx 4) is a 7-field tuple:

| Field | Bytes | Meaning |
|-------|-------|---------|
| 0 | 69 | board (chessValidator format, chess_script.md §1) |
| 1 | 5  | lastMoveSpec — the spec that produced this board |
| 2 | 32 | whiteHolderID |
| 3 | 0 or 32 | blackHolderID (`0x` until acceptance, then 32 bytes) |
| 4 | 4  | T_slots (BE u32) |
| 5 | 5  | deadline as `base.LedgerTime` (slot+tick) |
| 6 | 1  | flags — bit 0 `tieProposed`, bits 1..7 reserved (must be 0) |

Holder IDs follow the standard Proxima idiom (`hash(sigType‖pubkey)`),
the same value carried in `TxSignatureData` and used by sigLock,
tag-along, and delegation.

To prevent malicious behavior, UTXO cannot be discontinued until the end of the game. 
Any successor UTXO must have exactly the same tuple structure and non-decreasing balance. This must be enforced on the predecessor UTXO

## 3. chess() lock dispatcher

The lock at idx 2 is a thin dispatcher:

```
chess: callRedeemer(<chessGameHash literal>, byte(selfUnlockParameters, 0), …)
```

The first byte of unlock parameters is the **branch selector**:

| Selector | Branch |
|----------|--------|
| `0x00` | move |
| `0x01` | tie-accept |
| `0x02` | resign |
| `0x03` | timeout-claim |

The remaining unlock bytes are branch-specific (see §4). chessGame
reads the produced UTXO via `selfOutput`, the predecessor via the
chain constraint, and the tx signer via `txSignaturePubkey`. The
signer's holder ID is computed and matched against `whiteHolderID` /
`blackHolderID` per branch.

## 4. Branches

### 4.1 move (`0x00`)

Unlock params:
- byte 0 = `0x00`
- byte 1 = move flags — bit 0 `proposeTie`, bits 1..7 reserved (0).

Common preconditions on every `move`:
- `txSlot < predecessor.deadline`
- `boardOK(produced.board)`
- `produced.flags` low-byte bits 1..7 are zero
- `produced.T_slots == predecessor.T_slots` (T fixed at origin)
- `produced.whiteHolderID == predecessor.whiteHolderID`
- `produced.deadline == txSlot + produced.T_slots`
- `produced.lastMoveSpec` is the same 5-byte spec passed to
  `playerMove` in the case checks below

Three sub-cases on predecessor / chain origin:

**(a) Origin** — predecessor is the chain-constraint origin marker
(no real predecessor):
- `produced.whiteHolderID == hash(signerPubkey)`
- `produced.blackHolderID == 0x`
- `playerMove(WHITE, CANONICAL_START_BOARD, produced.lastMoveSpec,
  produced.board)` (CANONICAL_START_BOARD is the 69-byte literal from
  chess_script.md §1.1)
- `produced.flags == 0x00` (no tie offer at origin — there is no
  opponent yet)
- `produced.amount` is whatever white chose (≥ minimum storage deposit).

**(b) Acceptance** — `predecessor.blackHolderID == 0x` (move 2):
- signer's holder ID ≠ `predecessor.whiteHolderID` (white cannot
  accept their own game; everyone else is admissible)
- `produced.blackHolderID == hash(signerPubkey)`
- `produced.amount ≥ 2 × predecessor.amount`
- `playerMove(BLACK, predecessor.board, produced.lastMoveSpec,
  produced.board)`
- `produced.flags.proposeTie` may be set freely; `acceptTie` not
  applicable on this branch (covered by `tie-accept` branch §4.2)

**(c) Ordinary** — neither (a) nor (b):
- signer's holder ID == side-to-move's holder ID, where side-to-move
  is selected by `sideToMove(predecessor.board)`:
  - `0x10` → `predecessor.whiteHolderID`
  - `0x20` → `predecessor.blackHolderID`
- `produced.amount ≥ predecessor.amount` (monotone)
- `produced.blackHolderID == predecessor.blackHolderID`
- `playerMove(sideToMove(predecessor.board), predecessor.board,
  produced.lastMoveSpec, produced.board)`
- `produced.flags.tieProposed` is the current actor's choice (set or
  cleared independently of `predecessor.flags.tieProposed`).

### 4.2 tie-accept (`0x01`)

Unlock params:
- byte 0 = `0x01`

Preconditions:
- `predecessor.blackHolderID != 0x` (game must be past acceptance)
- `predecessor.flags.tieProposed == 1`
- signer's holder ID == `sideToMove(predecessor.board)`'s holder ID.
  After the proposer's move byte 68 of the board has already flipped
  to the opponent, so `sideToMove(predecessor.board)` *is* the side
  that did not propose — i.e. the accepter.
- `txSlot < predecessor.deadline` (same time bound as `move`)
- chain is **terminated** — no chain successor produced
- exactly two sigLock outputs are emitted with the chain's full
  amount split:
  - white sigLock keyed by `predecessor.whiteHolderID`, amount =
    `⌈predecessor.amount / 2⌉`
  - black sigLock keyed by `predecessor.blackHolderID`, amount =
    `⌊predecessor.amount / 2⌋`

The accepting tx does **not** play a chess move; the move would
make no protocol difference because the chain ends.

### 4.3 resign (`0x02`)

Unlock params:
- byte 0 = `0x02`

Preconditions:
- `predecessor.blackHolderID != 0x` (no opponent to resign against
  pre-acceptance — use `timeout-claim` instead)
- signer's holder ID == side-to-move's holder ID
- `txSlot < predecessor.deadline`
- chain is terminated; one sigLock output to the *opposite* holder ID
  with amount ≥ predecessor.amount

### 4.4 timeout-claim (`0x03`)

Unlock params:
- byte 0 = `0x03`

Preconditions:
- `txSlot ≥ predecessor.deadline`
- claimant identity:
  - if `predecessor.blackHolderID == 0x` (pre-acceptance abort): signer
    == `predecessor.whiteHolderID`
  - else: signer's holder ID ≠ side-to-move's holder ID (opposite side)
- chain is terminated; one sigLock output to the claimant with
  amount ≥ predecessor.amount

## 5. Transaction skeletons

### 5.1 Origin (`proxi chess new`)

```
inputs:
  - white's funding UTXO (sigLock)
unlock:
  - sigLock unlock for input 0
outputs:
  - chess UTXO (origin) with chessState[…], chain origin marker
  - any change output back to white
TxConstraints:
  - redeemScript(<chessValidatorBin>)
  - redeemScript(<chessGameBin>)
signature:
  white's ed25519 signature
```

### 5.2 Acceptance (`proxi chess accept`)

```
inputs:
  - origin chess UTXO
  - black's funding UTXO(s) (≥ white's stake)
unlock for chess input:
  - byte 0 = 0x00 (move), byte 1 = 0x00 (no tie propose)
outputs:
  - chess UTXO (move 2) with blackHolderID set, amount ≥ 2A
  - any change back to black, optional tag-along
TxConstraints, signature: as origin (signed by black).
```

### 5.3 Ordinary move (`proxi chess move`)

```
inputs:
  - chess UTXO (predecessor)
  - any tag-along source the player wants
unlock for chess input:
  - byte 0 = 0x00, byte 1 = 0x00 or 0x01 (proposeTie)
outputs:
  - chess UTXO (successor) with updated state
  - optional tag-along payment
signature:
  side-to-move's ed25519 signature
```

### 5.4 tie-accept / resign / timeout-claim

Same shell, but unlock byte 0 is `0x01` / `0x02` / `0x03` and the
outputs are sigLock(s) to the recipient(s) with no chain successor.

## 6. CLI surface (`proxi chess …`)

| Command | Effect |
|---------|--------|
| `proxi chess new --stake=<amt> --time-budget=<slots> --first-move=<uci>` | create origin (white) |
| `proxi chess accept <chainID> --first-move=<uci>` | accept (black); auto-stakes ≥ white's stake |
| `proxi chess move <chainID> <uci> [--propose-tie]` | normal move |
| `proxi chess accept-tie <chainID>` | take the tie-accept branch |
| `proxi chess resign <chainID>` | resign |
| `proxi chess timeout <chainID>` | claim opponent's timeout (or pre-acceptance abort) |
| `proxi chess status <chainID>` | print board, side-to-move, deadline, flags, holders, bounty |
| `proxi chess list [--mine]` | list active games (optionally filter to caller's holder ID) |

`<uci>` is standard 4–5-character UCI (`e2e4`, `e7e8q` for queen
promotion, `e1g1` for kingside castling); the CLI translates UCI to
the 5-byte chessValidator move spec, fills in flags (capture, castle,
en-passant, promotion piece) by inspecting the predecessor board.

## 7. Out of scope (v1)

Inherits all of chessValidator §6:
- 50-move rule, threefold repetition (no game history in fixed-size
  state).
- Insufficient material (FIDE auto-draw).
- Stalemate as draw — collapses to timeout under the deadline rule.
- Chess960 castling.

PoC-specific:
- Counter-tie offers: a player whose opponent proposed a tie must
  respond with either an ordinary move (clears the offer) or the
  `tie-accept` branch. Re-raising / counter-proposing in the same
  half-move is allowed simply by setting `proposeTie` again on a
  normal `move`.
- Multi-sig participants: each side is a single Proxima holder ID.
- Spectator deposits (third parties topping up the bounty): not in v1
  (only side-to-move signs successors).
- Mid-game T renegotiation: T is fixed at origin.

## 8. Implementation plan

The work splits into two phases. **Phase 1 lands and stabilises the
covenant on UTXODB only — no networking, no CLI.** Only after the
UTXODB suite is green and exhaustive do we move to Phase 2 (`proxi`
CLI on a real node).

### Phase 1 — covenant + extensive UTXODB tests

1. **chessGame EasyFL source.** Write the redeemer with branches
   `move`, `tie-accept`, `resign`, `timeout-claim`. Pin
   `chessValidatorHash` via `CompileLocalScriptWithCheck`. Reuse
   chessValidator's `boardOK`, `playerMove`, `sideToMove` via
   `callRedeemer`.
2. **State helpers in Go.** `ChessState` struct +
   marshal/unmarshal mirroring the idx-4 tuple; constants for branch
   selectors and flag bits; the canonical-start 69-byte literal as a
   `var`.
3. **TxBuilder helpers.** Functions `BuildOrigin`,
   `BuildAcceptance`, `BuildMove`, `BuildTieAccept`, `BuildResign`,
   `BuildTimeoutClaim`. Each pushes both `redeemScript`
   constraints, sets unlock bytes, updates chessState, and produces
   the right outputs.
4. **Extensive UTXODB tests** (in `examples/chess_poc/`, using the
   `ledger/utxodb` in-memory harness — fast, deterministic, no
   networking). Coverage targets:
   - **Per-branch goldens.** Origin / acceptance / ordinary move /
     tie-accept / resign / timeout-claim — each happy path.
   - **Per-branch negatives.** For every precondition listed in §4,
     one test that violates it and asserts rejection with a specific
     error string. Includes: wrong signer, wrong amount, wrong holder
     binding, wrong deadline arithmetic, illegal chess move (delegated
     to chessValidator), tampered T_slots, reserved-flag bits set,
     tie-accept without `tieProposed`, resign before acceptance,
     timeout claim before deadline, wrong claimant, etc.
   - **State-transition invariants.** `whiteHolderID` immutable across
     all transitions; `blackHolderID` immutable after acceptance;
     amount monotone non-decreasing; T_slots immutable; deadline
     equals `txSlot + T_slots` exactly.
   - **Mate-by-deadline e2e.** Fool's Mate (`1.f3 e5 2.g4 Qh4#`)
     across four chained txs; mated white cannot make a `playerMove`,
     so black settles via `timeout-claim` after T_slots. Validates the
     "no mate detection — timeout collapses it" rule.
   - **Tie-accept e2e.** White proposes on some move N; black takes
     the tie-accept branch; verify the two sigLock outputs and the
     odd-amount tail going to white.
   - **Resign e2e.** White resigns mid-game; black sigLock receives
     full bounty.
   - **Pre-acceptance timeout e2e.** White origin, no acceptance,
     white reclaims via `timeout-claim` after T_slots.
   - **Bounty-growth path.** A long game where amount strictly
     increases on some moves (player tops up); confirm covenant
     accepts and tag-along outputs are independent.
5. **Tx-size sanity check.** A representative chess move tx (both
   binaries committed via `redeemScript`, one input, one output, plus
   tag-along) must fit under the 65,531-byte network limit. This is a
   Phase-1 gate: if it doesn't fit, Phase 2 is moot.
6. Run some well known match in a UTXODB test

### Phase 2 — proxi CLI and live-node walkthrough

Only after Phase 1 is fully green:

6. **`proxi chess` subcommand tree.** Wire the Phase-1 builders under
   the commands in §6; add UCI ↔ moveSpec conversion (including
   castling / EP / promotion-piece detection from the board).
7. **Live-node walkthrough** against a single-node bootstrap (per
   `tests/README.md` standalone pattern): two wallets play a short
   game end-to-end through the CLI, including a tie-accept and a
   pre-acceptance timeout reclaim.

## 9. References

- chessValidator — `<easyfl repo>/chess/chess_script.md`,
  `<easyfl repo>/chess/chess_script.easyfl`.
- redeemScript / callRedeemer — `claude/archive/shipped/local_script.md`.
- UTXO tuple layout & holder-ID idiom — CLAUDE.md, "UTXO tuple
  layout" and "Single-signature transaction model".