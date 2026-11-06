# Ledger upgrade backlog

> **LIVE** — validity rules waiting for a library upgrade. Started 2026-10-26, when the
> release was kept Go-only. An item leaves this list when it is specified in its own
> `kb/library_upgradeN.md`. Revised 2026-11-01: two items went into upgrade 4, one was
> dropped, the fairness rule stays. Revised 2026-11-05: the controller rotation item went into
> upgrade 5. Revised 2026-11-06: the controller-side unfreeze added, phase B of
> `seq_decommission.md`.

A rule belongs here when the node already protects itself in `core/`, but the
ledger still accepts the transaction. Such a rule cannot be added in Go under
`ledger/`, not even in the stage-1/2 scans of `ledger/transaction`: nodes on
different releases would then disagree on validity. It has to be a library rule
that activates at a slot (`ledger/upgrade.md`).

## Taken by upgrade 4

Endorsement validity (every endorsement a sequencer transaction) and the chain-origin ban (a sequencer
output is not a chain origin) are specified and built in `kb/library_upgrade4.md`, in EasyFL, without
a builtin: an unroll over the eight endorsements needs none.

## Taken by upgrade 5

The controller rotation item below is specified and built in `kb/library_upgrade5.md`, active since
slot 153000: a sequencer chain moves to another key only in the first slot of a 60-slot window, and
also its output must be a `sigLock` and its chain predecessor a sequencer output, which closes two
ways around the lock comparison this item proposed.

## Dropped

- **Cross-slot chain predecessor needs an endorsement or an explicit baseline.** Already a ledger rule
  (`_crossSlotPredecessorCase` in `sequencer.easyfl`, full validation), and the node refuses such a
  transaction early (`Transaction.BaselineDirection`). Moving the check to the partial context changes
  no validity, and transactions reloaded from the transaction store skip the partial-context script.

## No own-branch extension right after the branch

Unlike the items above, not a rule the node already enforces in `core/`: a fairness rule, from the
2026-10-30 live measurement (`.internal/capital-bias.md`, gitignored, with the scripts in
`.internal/seq_analysis/`).

**Problem.** A slot's branches tie on coverage apart from the branch inflation bonus, which should
decide. The released sequencer (`sequencer/v1`) extends its own branch with its first milestone at
the post-branch consolidation tick; that milestone carries the issuer's whole coverage (balance and
frozen delegations), and a sequencer moving after it joins the heaviest lineage. Live, the bonus
decided 59% of tied slots; the founder with the largest capital won 57 slots against the bonus, all
other issuers 8, in capital order, with a capital lead of 1-3%. `sequencer/v2` moves at that tick
and chooses its first move by coverage among every extend-endorse pair, staying on its own branch
included (`factory/first_move.go`): it backs its own branch only when that has the most coverage. If
every sequencer does, the bonus decides and a self-backing issuer wins only with more capital than all
those who followed the bonus.

**Rule.** A sequencer transaction other than a branch, timestamped before tick
`constPostBranchConsolidationTicks + k` of its slot, may not have as chain predecessor a branch of
the same slot (its own branch: a chain predecessor is always the own chain). Other sequencers'
milestones may extend or endorse any branch from the post-branch consolidation tick as now, so the
choice stays pure coverage maximisation; the coverage definition is not touched. In the window the
issuer can still build on another lineage from its older chain output (the re-anchor `v2` does).

**Value, and its limit.** It stops a single chain from backing its own branch before the others
choose, whatever software it runs. It does not stop a holder running a second sequencer chain that
extends the branch in the window: the ledger cannot tell owners apart, so the split capital backs the
branch just the same. It raises the cost of self-backing, it does not remove it. The main remedy is
every sequencer moving at the post-branch consolidation tick and choosing by coverage (`v2` as the
default); this rule is a guard behind it. Decide after measuring a network of mostly `v2`.

**Measured, not taken into upgrade 4 (2026-11-01).** With `v2` on every founder the bonus decided
95-97% of tied slots and the largest issuer had 9% of wins: the capital bias is gone without the rule.
Its cost: every sequencer version (`v1`, `v2`, `v3`) extends its own branch at the post-branch
consolidation tick, so all three, the attacher's timestamp floor and the slot timing would change, and
a lone sequencer could not move before tick 9+k on its own lineage.

**Shape.** An extension of the sequencer constraint in `sequencer.easyfl`, next to the post-branch
consolidation rule of upgrade 2 (`kb/library_upgrade2.md`): the chain predecessor's transaction ID
is in the input, so whether it is a branch (a flag in the ID) and its slot need no builtin. `k` a library constant, a few ticks; the attacher's timestamp floor and the
sequencer's first-move timing follow it.

## Controller rotation grinds the branch bonus

Added 2026-11-03. Measured live the same day: not exploited. **Taken by upgrade 5** (section above);
kept here for the options considered and the measurement.

**Problem.** A branch's VRF proof is checked under the key that signs the transaction
(`stemLock`, `upgrade3.easyfl`), and that key is the holder of the `sigLock` on the
branch's chain predecessor. Nothing pins that lock across a chain transition
(`control_succession.md`), so any milestone may move the chain to another key. The VRF
message is `vrfOutput(predecessor stem) || slot`, which everyone knows from the moment the
previous branch exists. An operator with N keys therefore computes, during slot s-1, the
bonus every key would get in slot s, and sets the lock of its last milestone in s-1 to the
best one. Branches tie on coverage apart from the bonus, so with M other branching
sequencers, the operator wins about N/(N+M-1) of the slots instead of 1/M. The keys cost
nothing.

**Options considered.**
- *Lock change only at one point in the slot (the first milestone, ticks 9-16, or tick 9).*
  Does not help. The next branch's randomness is known a full slot ahead, so whenever the
  change happens the operator still sees every key's bonus for the next branch.
- *Delay before a new key may sign a branch.* Not enough on its own. With delay D, the
  operator can't predict the branches between, but along a path where it wins those itself
  it computes its own outputs, so while it keeps winning, each change is ground ahead of time.
- *At most one change in a minimum number of slots, stored in the output.* Works: one choice
  per period, worth about one slot. Needs a new state field on every milestone, which means
  changing the arity of `sequencer` across an upgrade, while chain outputs made before it
  still carry the old form.
- *VRF key fixed at the chain origin* (its holder ID kept unchanged in the chain, the VRF
  proof checked under a public key on the stem). This stops the attack completely, but it
  adds 32 bytes to every milestone and a second key per chain, and a lost VRF key leaves a
  chain that can't branch.

**Rule (preferred).** A sequencer chain transition whose produced lock differs from the
consumed lock is valid only in the first slot of a 60-slot window, the windows counted from
the start of the delegation epoch: `isZero(mod(txSlot, constControllerChangeSlots))` with a
new library constant `constControllerChangeSlots = 60`, a divisor of
`constDelegationEpochSlots` (600), so every epoch starts a window. That is one slot in every
60, about once per 10 minutes. No state, no new bytes, pure EasyFL in `sequencer`: compare
the lock elements of the chain predecessor and of the successor. The operator still gets one
free choice per window. That is worth about one branch out of 60, plus a short run of wins
along its own path, against N/(N+M-1) of all slots without the rule. A controller change
waits at most about 10 minutes.

**Consequences.** `control_succession.md`, not built, would have to time its claim (the
handoff milestone) for the first slot of a 60-slot window. The sequencer's own milestones re-emit the
predecessor lock, so the sequencer itself does not change. Chain origins are not
affected: a new chain is already a new ticket.

**Residual.** N sequencer chains are N tickets whatever the lock rule says. That costs a
chain per ticket, its storage deposit, and running a sequencer that consolidates the whole
ledger every slot. Unlike rotation it is not free, and no rule on keys closes it.

**Measured 2026-11-03: not exploited.** Every branch of every 20th slot in 127941-130841
(146 slots, 4,186 branches, 33 sequencer chains), read from the transaction store of the
public nodes with `dag_explorer/find_tx?q=<slot>-0` and `dag_explorer/tx_detail`, which gives
each branch's signing holder ID, the sigLock holder of its produced sequencer output and its
bonus (produced amount 1 of that output):
- every chain was signed by one key throughout, and no branch moved its chain's lock;
- the mean bonus per chain lay between 2.03M and 2.75M, within 2 standard errors of the
  2.5M of one honest key (two keys would give 3.33M); over all branches 2.51M, 20.4% above
  4M (20% expected).

The scan was stopped early, so the sample covers about the first 8 hours of the 24 h asked
for. It is still enough: grinding needs a change at almost every branch, and every sampled
branch of every chain would have shown it. Sampling every 20th slot can miss a key held
for less than 20 slots and then reverted, but that buys almost nothing.

The `BRANCH COMMIT` log lines cannot answer this question. A node commits only some of a
slot's branches (4 of the 25 in slot 136373 on hloc0-acc), and the ones it commits skew to
high bonuses: from the logs, our own founders averaged 3.5-3.8M. Read the branches from the
transaction store.

## Controller-side unfreeze

Added 2026-11-06. DRAFT, phase B of `seq_decommission.md`, whose phase A (the `max_frozen` bound)
ships without a ledger change. Unlike the items above, this is not a rule the node already enforces.
It is a new permission the ledger has to grant, so it can only arrive with a library upgrade. It is one
replaced function and touches nothing in upgrade 6, so it can ride with whichever upgrade is listed
next.

**Problem.** With the bound at 0 a sequencer winds down in up to 60 epochs, about 4.3 days. To go
faster, its controller needs to stop the freezes it holds without losing the unearned part of the
advances. Today the target may put a frozen delegation on hold without the master
(`_requireUnlockableByTheTarget`, upgrade 3), but on that 2-byte path the balance may not decrease.
Only the master's allowance (askstop, the 3-byte path) lets the target take anything back, so a
sequencer that puts a delegation on hold unasked leaves the whole advance with the delegator.

**Rule.** One function of `lock_delegate.easyfl` is replaced in the upgrade library, same arity. On
the 2-byte target path, a delegation frozen in the transaction and put on hold may give back at most
its unearned advance:

```
// the part of the advance the remaining frozen span will not earn, at the share
// pinned when the freeze began, measured from the transaction's slot
func _unearnedAdvance :
   _takeCut(chainInflationMultiStep(selfTokenBalanceValue, txSlot, _txFrozenSlots), _selfAdvanceShare)

// Replaced: on the 2-byte target path, a frozen delegation put on hold may give
// back up to its unearned advance; any other 2-byte transition still may not decrease.
func _validAmountOnSuccessor :
if(
   equal(len(selfUnlockParameters), u64/3),
   if(_refIsTopUp, _topUpAuthorised($0), _decreaseWithinAllowance($0)),
   if(
      and(_consumedIsFrozenInTx, _successorIsOnHold),
      require(lessOrEqualThan(_amountDecrease, _unearnedAdvance), !!!decrease_exceeds_the_unearned_advance),
      require(lessOrEqualThan(selfTokenBalanceValue, _amountOnSuccessor), !!!delegated_amount_should_not_decrease)
   )
)
```

It is the askstop compensation (`_projectedCompensation` in `ensure.easyfl`) measured from the
transaction's slot instead of the delegation output's slot. Askstop anchors at the output's slot
because the delegator signs before the sequencer picks the request up. Here the sequencer computes
the figure for its own transaction, so the true quantity can be used, and it is never above the
askstop ceiling. The target's frozen-coverage debit is unchanged: it is computed from the frozen
predecessor (`MakeFrozenCoverageAmountDeltasForRevoking` in
`evalEnforceFrozenCoverageOnDelegateOutput`), so it takes back exactly what the freeze credited,
whatever balance the on-hold successor keeps.

**What changes for delegators.** A frozen delegation used to guarantee the delegator the whole
advance even if the target stopped it early. From the upgrade on it guarantees the advance earned
up to the stop. This is the same unwind askstop applies the other way: both sides bear their own
foregone inflation and neither compensates the other. A target stopping a delegation early gains
nothing by it: it gives up the frozen coverage, and with it the inflation that would have repaid the
rest of the advance. Nor can a target use the rule to rent coverage cheaply over and over: once on
hold, a delegation can be unlocked only by its master, so each freeze-and-stop cycle needs the
master to re-delegate.

**Node and wallet, once the rule is listed.**

- `proxi node seq unfreeze <delegation ID> [...] | all`: one tag-along request from the controller
  to its own sequencer, naming up to 20 frozen delegations that target it. It carries no guarantor
  (`ensureStopDelegation`): the authorisation is the controller's, and the bound is in the delegate
  lock. `all` takes the longest remaining frozen span first, since that carries the largest unearned
  advance, and skips delegations within 6 slots of unfreezing, as askstop does. The wallet refuses
  unless `max_frozen` is 0, since otherwise the sequencer would freeze new delegations while it
  stops the old ones; the sequencer itself does not check it.
- Sequencer request code 7; codes 5 and 6 are reserved by `control_succession.md`. The layout is
  that of set-params: exactly 4 elements, request data with the code and field `'i'`, the concatenated
  IDs (`n x 24` bytes, `1 <= n <= 20`). The balance is the minimum fee, which goes back to the chain.
- `txbuilder_seq/req_unfreeze.go`, modelled on `req_askstop.go`. **Parse:** refuse unless the
  sender is the controller and the library has the upgrade at the transaction's slot; before
  activation the request is refused as permanently invalid and can be reclaimed after
  `constTagAlongSlots`. **Apply:** for each ID, read the delegation from the baseline state; skip it,
  logged, unless it targets this sequencer, is in a frozen slot and is not already an input of the
  builder. Put each remaining one on hold with `MakeDelegationRevokeOutput`, `TakeFromBalance` the Go
  mirror of `_unearnedAdvance`, and the 2-byte unlock. The request is consumed even when nothing was
  left to stop. Attachment cost 1 + 2 per delegation, at most 41. The delegation pool already
  records target-side unfreezes (`transitionUnfreeze`).
- **After submitting**, the wallet tracks inclusion and waits, for at most `constTagAlongSlots`,
  until the request output is consumed in the LRB state. It then lists the delegations again and
  reports what is still frozen, count and amount. If any remain, it says to run `unfreeze all`
  again. If the request was not taken, it says why that may be (node not running, upgrade not
  active) and that the request can be reclaimed.
- **Askstop.** The sequencer takes from the delegation only the part of the compensation the request
  output does not cover, so a delegator paying from the wallet, as older `proxi` builds do, is not
  charged twice. That is sequencer policy: a dishonest sequencer can charge twice, but never more than
  the unearned advance.
- **`seq info`.** The decommissioning block of phase A also shows the unearned advance recoverable
  now.
- **Pacing.** Every round takes its delegations out of coverage at once. On a sequencer holding a
  large share of the network's frozen coverage, several rounds in quick succession can push branches
  under the health threshold, like the epoch-boundary dips. The runbook should say to watch
  `proxima_lrb_coverage` between rounds.

**Tests.** In `ledger/tests`:
- A frozen delegation put on hold by its target with a decrease equal to `_unearnedAdvance` passes,
  and one mote more fails.
- A decrease is still refused on the hold of an unfrozen delegation and on a top-up continuation.
- The askstop allowance path is unchanged.
- Before the upgrade slot, any decrease on the 2-byte path fails.

In `txbuilder_seq`: an unfreeze request from anyone other than the controller is refused. One from
the controller stops the named frozen delegations, skips the others and consumes the request. In
`tests/`: two rounds of `unfreeze all` on a sequencer with more than 20 frozen delegations.
