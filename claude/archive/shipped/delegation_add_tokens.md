# Top-up: adding tokens to an existing delegation

Status: **spec, not implemented.** Written 2026-08-15. Follows
`claude/delegation_scalability.md`, which establishes why this matters: delegation count
drives permanent state growth (~1–1.8 GB at 900 000 delegations vs ~2–4 MB at 2 000), and §9
there makes an early stop a pure unwind, so this loop costs only fees.

**No ledger change is required.** Everything below is builder, CLI and miner work.

The operation is called **top-up** throughout: moving wallet tokens into an existing
delegation and re-delegating it in one transaction.

---

## 1. When a top-up is possible

A delegation can be topped up exactly when the master can consume it —
`IsUnlockableByMaster(txSlot)`, i.e. `!IsInFrozenSlot(txSlot)`. Three ways to be there:

| State | How it arises | Cost |
|---|---|---|
| **on hold** | an askstop was processed, or the target never re-froze it | tag-along fee |
| **in the safe revocation window** | the freeze expired naturally; 60 slots ≈ **10.2 min** in which the target *cannot* re-freeze | tag-along fee |
| **undef** | freshly delegated, or refused at the target's cap | tag-along fee |

Anything else is a live freeze, and reaching it early costs an `askstop`.

**A top-up restores coverage, it does not interrupt it.** While a delegation sits unfrozen
its capital counts for nothing: `PastCone.CoverageDeltaRaw` accumulates only over *consumed*
outputs, a frozen delegation reaches coverage through the sequencer's `AdjustedFrozenCoverage`
rather than through itself, and once the freeze expires it is in neither place until
something consumes it (`claude/delegation_scalability.md` §3). Topping up is such a
consumption. So acting inside the window is strictly better for the network than letting it
lapse, and there is no coverage argument for delaying.

### Why the ledger already allows it

- `_validAmountOnSuccessor` forbids a *decrease* without an allowance; an **increase is
  unconstrained**.
- `_masterUnlockedConsumed` requires `not(_consumedIsFrozenInTx)` — the master may consume
  any delegation that is not currently frozen.
- `_requireUnlockableByTheTarget` refuses on-hold outputs to the target, so an on-hold
  delegation belongs to the master until re-delegated. No race with the sequencer.
- The successor takes the `undef` arm of `_validLimitsProduced`: state mark 0,
  `lastFrozenEpoch` 0, zero frozen-coverage vector. The target freezes it again afterwards.

`proxi node delegate chain` already accepts a delegation output as its predecessor
(`predIsDelegation`), so only the extra wallet inputs are new.

---

## 2. The free window

A freeze runs **60 epochs ≈ 4.27 days**, then the safe revocation window opens for
**60 slots ≈ 10.2 min**. Per delegation that is a duty cycle of 0.17 %, so no single
delegation is usefully available on demand.

**The sequencer spreads them.** `latestArgminUnderCap` places each freeze in the latest
*least-loaded* epoch of the reachable window, crediting `D[i] += amount` within the pass so
delegations frozen in the same milestone land in different epochs, and rebalancing on every
re-freeze so any concentration self-heals. Delegations pointing at one sequencer share only
the offset *inside* the 600-slot epoch; their unfreeze times differ by whole epochs, 1.707 h
apart, across a 60-epoch span. Diversity comes from the sequencer's own load balancing, not
from using different targets.

Fixing the freeze depth at 60 widened this: `reach` used to be the delegation's own
`maxFrozenEpochs` capped at the chain's, so a delegator picking 8 was confined to 8 epochs.
Every delegation now spreads across the full 60.

So a holder with `n` delegations sees roughly `n` windows per 4.27-day cycle, whether or not
the targets differ:

| Delegations | Expected wait for a free window | One open right now |
|---|---|---|
| 1 | 4.3 days | 0.17 % |
| 5 | ~20 h | 0.8 % |
| **10 (default cap)** | **~10.3 h** | **1.7 %** |
| 20 | ~5.1 h | 3.3 % |

At a random instant the chance that *any* of `n` delegations is inside its window is tiny —
1.7 % at `n = 10` — so a holder that wants to act now will almost always find none open. The
question is therefore not "is a window open" but "is waiting cheaper than paying". §3 works
that out, and the answer is usually **paying**.

One transient worth knowing: `n` delegations created and first frozen together land in `n`
*consecutive* epochs at the far end of the window, so their first cycle gives `n` windows
1.7 h apart followed by a long gap. Rebalancing on re-freeze spreads them out from there.

---

## 3. The rule

Given an amount `A` to delegate, current delegations `D` (count `n`), cap `C`:

1. **An accessible delegation exists** → top up the smallest one.
   *Fee only.*
2. **Otherwise, `n < C` and `A` clears the ledger's minimum delegation size** → create a new
   delegation.
   *Fee plus a storage deposit, which is recoverable — so this beats askstop outright. The
   minimum is `storageDeposit` of a delegation output, computed wallet-side, not configured.*
3. **Otherwise** → **askstop** the nearest-window delegation. The next pass finds it on hold
   and takes step 1.

There is no waiting branch and no threshold. Step 1 is opportunistic: take a window if one
happens to be open, otherwise pay. See below for why paying is cheap enough that optimising
the choice is not worth the complexity.

### Why askstop is cheap

**The unwind is not a cost.** It returns a prepayment the delegator has not earned, and the
next freeze pays a *fresh* advance over a full new span — every freeze does. Returning an
unearned prepayment and immediately receiving a new one is net-neutral. (This holds only
because the loop re-delegates at once. Askstop and walk away, and the unwind *is* a real loss
against letting the freeze run.)

What is actually lost is time spent unfrozen, `amount × slots / (m0 + s)`. The askstop round
trip — request, sequencer puts it on hold, top-up confirms, target re-freezes — is of **order
10 slots**, so at 200 000 PROX it costs about **0.05 PROX**. Waiting ~10 h for a window
instead would idle 100 000 PROX for ~3 600 slots, about **12 PROX**.

Askstop is two orders of magnitude cheaper than waiting for a window. That is why there is no
threshold to compute: within a window, use it; outside one, pay.

### The one flag: whether to use windows at all

The safe revocation window is the **only** period in which the master can act and the target
cannot — the constraint locks the target out entirely
(`delegation_cannot_be_unlocked_by_the_target_in_safe_revocation_window`). It is therefore the
delegator's escape from a sequencer that simply declines to include askstop requests;
declining is always possible, since inclusion cannot be forced.

A miner that takes those windows leaves its owner none. The fix is a single flag:

| `mine.delegate.use_revocation_windows` | behaviour |
|---|---|
| `true` (default) | step 1 accepts on-hold, undef **and** in-window delegations |
| `false` | step 1 accepts only on-hold and undef; natural windows are left untouched, and the miner always askstops |

Nothing else changes between the modes, and the cost difference is ~0.05 PROX per cycle. An
owner who wants a guaranteed way in sets it false; the fallback either way is to stop the
miner and wait up to **4.27 days** for the next window.

**There is no race.** The target cannot re-freeze while the window is open, so a top-up
submitted inside it cannot be beaten by the sequencer. The only exposure is a transaction
submitted so late that it confirms after the window closes — ordinary submission margin, not
a design problem.

### Retargeting is free, and every top-up is an opportunity for it

On the master path the constraint does not pin the index-value tuple; only
`_targetUnlockedConsumed` requires it to match. So the master may name a **different target
sequencer** on the successor. A top-up therefore erases the delegation's history: what comes
out is a fresh `undef` delegation whose target is whatever the master chose, and the old
sequencer has no claim on it.

The miner picks with `chooseRandomAliveSequencer` — uniformly among sequencers whose latest
output is within `aliveSequencerSlots` (**2**) of now, falling back to whichever produced most
recently if none qualifies. A running sequencer produces a milestone most slots, so 2 is
already generous; a wider window mostly admits ones that have just stopped. The fallback
matters because failing here would strand the payouts undelegated, whereas delegating to a
sequencer that turns out to be stalled is self-correcting — the delegation stays unfrozen and
the next pass re-rolls the target.

Keeping random selection on each re-delegation is right:

- it spreads delegations over sequencers without coordination, which is what the per-epoch
  cap needs (`claude/delegation_scalability.md` §6);
- it avoids lock-in to one sequencer, and to its policy on askstop;
- **refusal self-correction falls out for free** — a sequencer at its cap refuses the freeze,
  the delegation stays undef, and the next pass re-rolls the target rather than retrying the
  same one.

---

## 4. The four surfaces

**`delegate chain --add <amount>`** — the existing command gains an optional top-up:
consume the chain output plus enough wallet inputs, produce a delegation of
`chainBalance + inflation + amount`, remainder back to the wallet. `--add 0` is today's
behaviour exactly.

The predecessor need not already be a delegation — `delegate chain` takes **any** chain, and
`--add` changes nothing about that. Two details keep it general: the wallet-input query
excludes chained outputs, so the predecessor cannot be consumed twice when it is a plain
sigLock chain of the same wallet; and the first wallet input carries its own signature unlock
instead of referencing input 0, which would be invalid when input 0 is a `delegateLock`, since
reference unlock holds only within one lock kind.

While here: **take the tag-along fee from the wallet inputs, not the chain balance.** The
command currently computes `newAmount = balance + inflation - feeAmount`, silently shrinking
the delegation on every transit. With wallet inputs present there is no reason to.

**`proxi node dlg topup <amount>`** — **does what it is told.** The constant in §3 exists so
an unattended miner has an answer; a person typing a command has already decided. If they ask
to stop a delegation, stop it.

What the command owes them is the *numbers*, before it acts: which delegation, how long until
its window would have opened anyway, what the unwind returns and that it is repaid at the
next freeze, and the fees. Then one `glb.YesNoPrompt` in the shape of
`confirmDelegationEstimate`, defaulting to no. Report why an option is unavailable ("cap
reached", "none accessible") rather than hiding it — that is how the reader learns the model.

The one case worth a warning rather than a number is stopping a delegation whose window opens
within a few minutes: pointless, and the tool should say so.

**Auto-redelegation** — a controller-side sweep, shared by proxi and the miner: list own
delegations, select those with `IsUnlockableByMaster(now)`, top up and re-delegate. Same
target sequencer by default; retargeting is a separate decision and should not be bundled.

**The miner** — runs §3 unattended at each consolidation opportunity, with the accumulated
payout UTXOs as `A`. Step 1 is the normal path; step 2 fills the cap early on; step 3 is
rare. The (askstop → next pass tops up) chaining needs no extra state, because §3 re-derives
what to do from the ledger each pass.

Selection when several candidates exist: **top up** the smallest delegation, so balances even
out; **askstop** the one nearest its natural window, because that unwind is cheapest. Target
choice for a new delegation is a separate question — the sequencer's load balancing already
supplies the scheduling diversity, so pick on the usual grounds (a live target with room
under its per-epoch cap), not to spread phases.

---

## 5. Costs

| Action | Cost |
|---|---|
| top up an accessible delegation | tag-along fee |
| create a new delegation | tag-along fee + storage deposit (recoverable) |
| askstop, then top up | 2 × tag-along fees + `(B+A) × δ × r` of foregone inflation |
| wait for a window | `A × t_w × r` of foregone inflation |

The unwind appears in none of these: it is returned prepayment, replaced by a fresh advance at
the next freeze. The only irrecoverable quantities are fees, storage deposits (until the
delegation is closed) and time spent unfrozen.

## 6. Config

Every timing threshold in this spec is derived, not configured. §3 decides wait-vs-askstop
from the balances and remaining freeze times already on the ledger, and the minimum size for
a new delegation is `storageDeposit` of a delegation output, which the wallet computes. There
is nothing to tune and nothing to get wrong.

The principle, and it is worth keeping: **a threshold that trades only the holder's own costs
can always be derived, so it should be.** A knob is warranted exactly where a cost falls
somewhere else.

| Key | Default | Meaning |
|---|---|---|
| `delegate.max_delegations` | **10** | advisory cap on delegations per wallet |
| `mine.delegate.top_up` | true | miner runs §3 at each consolidation opportunity |
| `mine.delegate.use_revocation_windows` | true | may the miner consume a natural revocation window, or must it always askstop |

That leaves three, and none of them is a threshold. `max_delegations` bounds **state size**, a cost
carried by every node rather than by the holder — the holder's own economics push the other
way, since creating is the cheapest of the three actions. Nothing private prices it, so it
cannot be derived and has to be a convention; it is advisory because exceeding it harms the
network slightly, not its owner. `top_up` is a mode, not a threshold: whether the miner delegates its payouts at all,
alongside the existing `consolidate` and `stash`. `use_revocation_windows` is likewise not a threshold
but a claim on a shared resource — the owner's only guaranteed way past a sequencer that
refuses askstop. Both are worth ~0.05 PROX per cycle to the miner and much more than that to
its owner, which is why neither is derived.

## 7. Open

- **Input count when sweeping many payouts.** A miner at the cap may hold hundreds of small
  payout UTXOs; the 256-input limit and the attachment cost budget bound one transaction, so
  the sweep may need batching.
- A stale non-zero `AdvanceShare` on an `undef` successor is unconstrained. Nothing reads it
  before the next freeze overwrites it — hygiene, not a defect.
