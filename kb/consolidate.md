# `proxi node consolidate` — permanent wallet consolidation

> **LIVE** — approved 2026-09-15, built; the delegation mode (§2.4b) was
> redesigned 2026-09-19 into a market mode driven by a target number and a
> target size of delegations, price-taking on the sequencer's cut, and tidying
> the existing set. The command is written
> independently of `proxi node mine`, which stays untouched for now: it is
> committed to `develop` first and tested on the testnet; only then are the
> miner's treasury loop (`mine_treasury.go`, `mine_topup.go` and the
> `--compact-at`, `--delegate*`, `--reserve`, `--max-delegations`, `--cut`,
> `--minimum_cut`, `--no-revocation-windows` flags) and the
> `proxi node compact auto` stub retired in a separate step (§5).

Date: 2026-09-15

---

## 1. Why

People run independently written miners against the mine chain. Those miners
do what the ledger requires and nothing more: every confirmed transit leaves a
payout on a plain sigLock output, and the miner never touches it again. The
result is a growing pile of sigLock UTXOs that sit outside consensus. Tokens on
a sigLock output are neither delegated nor held by a sequencer, so they add
nothing to ledger coverage, and every one of those outputs is permanent state
the whole network carries. Ledger health drops as mining succeeds.

`proxi node mine` handles this for its own payouts with a treasury loop, but
that helps only people who mine with `proxi`. The job belongs to the wallet,
not to the miner: a permanent, key-holding process that watches the account
and periodically sweeps what has accumulated, either back into consensus
(sequencer or delegation) or at least into one output. That process is
`proxi node consolidate`. It works for any wallet, whatever put the UTXOs
there, and it is what the documentation tells miners to run.

## 2. What it does

A long-running command on the wallet profile. Every tick it reads the
account, decides whether there is enough to act on, builds at most one
transaction, submits it and logs what it did. It never waits for inclusion: the
next tick sees the result in the account.

### 2.1 Consolidatable outputs

Two kinds, and only these two:

- plain **sigLock** outputs of the wallet;
- **tag-along outputs the wallet sent** whose target sequencer never took
  them, once the tag-along window (`tag_along_slots`) has passed, so the
  wallet can reclaim them.

Both are classified with the wallet library's spendable classifier at the
current slot, exactly as `proxi node compact` does. `sendWithDeadline` outputs
are deliberately left out: reclaiming or accepting them is a one-off decision
that stays with `proxi node compact`. Outputs of unrecognized structure are
never consumed.

### 2.2 When it acts

Four numbers from the profile: the **threshold** `H` above which the account
is worth acting on (default 1000 PROX), the **minimum balance** `M` the wallet
keeps on sigLock outputs (default 100 PROX), the **input cap** `N` (default
30) and the **compaction threshold** `P` (default 10 outputs).

Let `T` be the total of every consolidatable output and `n` their number. The
process acts when either

    T > H and n >= 2   (enough has accumulated, and it is scattered), or
    n >= P             (a pile of outputs is worth folding whatever it holds)

and otherwise does nothing this tick. A single output above the threshold is
not scattered and is left alone. When only the second condition holds the
transaction is a plain compaction, never a transfer or a delegation: nothing
has accumulated to move, and small-output piles are exactly the problem this
command exists for.

### 2.3 What it consumes

Up to `N` consolidatable outputs, **smallest balance first**. Small outputs
are the ones worth removing: each costs the network the same to carry whatever
it holds. Whatever does not fit is picked up by a later tick.

Let `C` be the total of the consumed set. The wallet keeps

    kept = max(0, M - (T - C))

as a single sigLock output: the minimum is a property of the whole account,
so outputs left unconsumed count toward it. Everything else in the consumed set
is what moves:

    moved = C - kept - fee

Below the threshold `moved` is zero: the whole consumed set is kept. `fee`
is the tag-along fee of the transaction, resolved as everywhere in proxi: the
larger of the profile `tag_along.fee` and the minimum declared by the
sequencer that receives the fee. If `moved` is not positive the transaction is
a plain compaction (§2.4c).

The tag-along target and its fee are resolved **before every transaction**,
never once at startup: the process outlives a sequencer's activity and a fee
setting, and a transaction built on stale values is never picked up. A
`random` target is drawn among the sequencers active now; a configured one
must be active now, else the tick is deferred (logged once until it is usable
again).

A `kept` output can come out tiny when `T - C` is just under `M`, below the
storage deposit the ledger requires of a sigLock output (about 9.25 PROX;
read from the node at startup, and `minimum_balance_prox` must be at least
that). Rather than fail, a remainder under that floor is folded into `moved`:
the wallet is then briefly a little under its minimum, which the next incoming
output restores. Symmetrically, a `moved` amount under one PROX (a few dust
outputs consumed while the rest of the account covers the minimum) is not
worth sending or delegating and stays in the wallet, so the transaction is a
plain compaction; if that compaction could not itself reach the floor, nothing
is built until more has piled up. Every sigLock output the process produces is
checked against the floor at build time, so a transaction the ledger would
refuse is never submitted, and never rebuilt and refused again each tick.

Both triggers require at least two outputs and the input cap is at least two,
so every transaction consumes at least two outputs: an account already
consolidated costs nothing per tick.

### 2.4 Where the moved tokens go

Decided from the `consolidate` section of the profile. Sending, when
configured, takes precedence over delegating. A mode that is configured but
cannot be applied this tick is logged, and the outputs are compacted instead
(c): a configured transfer never silently turns into a delegation.

**(a) Send to a sequencer** — `send_to_sequencer` is `own` or a sequencer ID.

- `own` means `wallet.sequencer_id`. Before every transaction the process
  reads that chain's output and checks that its lock is a plain sigLock whose
  holder is this wallet: the same read `proxi node seq info` shows as
  "controller lock". A sequencer the wallet does not control is refused with
  an error, and the tick compacts instead. The check is repeated every time
  because control can change hands while the process runs. An unset
  `wallet.sequencer_id` disables the mode with a warning at startup.
- a sequencer ID means any sequencer. It is checked once, at startup, to
  exist on the ledger and to be a sequencer chain, like the tag-along target.
- empty or unparsable disables the mode. An unparsable value is reported at
  startup so a typo does not silently turn sending off.

The target must be **active**: the latest milestone the node knows of it
lies within the last 3 slots (the node's known-sequencer-milestones endpoint,
the same source `tag_along.sequencer_id: random` uses). An inactive target
means the tokens would sit unclaimed in a tag-along output; the process logs
it and falls through.

The transfer is a **tag-along output** to the target carrying `moved`, with
the wallet as sender, which is how proxi always pays a chain (`chainLock` is
never produced: the wallet can reclaim a tag-along the sequencer does not
take). That output is also the sequencer's fee, so no separate fee output is
built and `fee` in §2.3 is the target's minimum, which `moved` clears by
construction. Outputs: the tag-along, then the `kept` sigLock if non-zero.

**(b) Delegate** — `send_to_sequencer` is off and `autodelegate` is `random`
or a sequencer ID. Redesigned 2026-09-19 as a market mode; the earlier
"miner's algorithm, moved here" is gone.

Two numbers drive the delegation set: `target_delegations` (default 5) and
`target_delegation_prox` (default 10,000 PROX). Delegations grow to the target
size one at a time, then their number grows to the target, then the existing
ones are topped up. Placing `D`:

1. a consumable delegation below the target size → add `D` to the smallest
   such one and re-delegate it;
2. otherwise, fewer delegations than the target → create a new one of `D`;
3. otherwise, a consumable delegation → add `D` to the smallest one;
4. otherwise → askstop the frozen delegation nearest its natural window,
   paying the compensation from the consumed set; a later pass takes step 1.

Consumable means the master can spend it in this slot: on hold, never frozen,
or inside its safe revocation window. Frozen delegations are left to their
target.

**Price taker.** A delegation requires exactly the cut its target leaves
(1000 minus the sequencer's own cut), read off the sequencer's output when the
transaction is built. `delegate.minimum_cut` is not read. With `random` the
target is drawn among the sequencers active within the last 3 slots (§2.4a)
**in proportion to what each leaves**: a sequencer keeping 40% is drawn 600
times out of 1600 against one keeping nothing, and one keeping everything is
never drawn. With a sequencer ID the target is that sequencer, which must be
active and leave something, or the action is deferred with a log line.

**Tidying.** Every tick opens by tidying the consumable delegations, one
action per tick and before anything is swept, so a wallet that always has
something to sweep cannot starve it; the tag-along fee is taken out of the
delegation itself:

- more delegations than the target → the smallest consumable one is folded
  into the largest consumable one, which is re-delegated with the combined
  balance; the smaller chain ends;
- otherwise, a consumable delegation that is stale is re-delegated as it is:
  its target is not active, or leaves less than the delegation requires (the
  sequencer refuses it as loss-making), or is not the configured one, or the
  delegation has sat unfrozen for longer than an epoch.

This is what brings a delegation set built under other rules — by the earlier
version, by `proxi node mine`, at another cut, on a sequencer that has since
raised its cut — into line without anybody touching it. A re-delegation whose
balance less the fee would fall under the minimum inflatable amount is
deferred; a fold never is, since the result is larger.

A new delegation of `D` below the minimum inflatable amount is not created;
the tick falls through to plain compaction and `D` accumulates. The
delegation output, the tag-along fee output (to the profile's tag-along
sequencer) and the `kept` sigLock are the outputs; the change convention of
the miner (one sigLock, nothing when zero) is kept. A transaction that
consumed delegations is tracked in flight (§2.5) through the delegation
outputs it consumed.

**(c) Compact** — neither mode applies, or `moved` is not positive.

One sigLock output back to the wallet holding `C - fee`, plus the tag-along
fee output. This is `proxi node compact` without the prompt and without the
inclusion wait; it shares `MakeCompactTransaction`.

### 2.5 In flight

Consumed outputs stay in the node's account snapshot until the transaction
settles, so the process remembers the IDs it consumed and stands still while
any of them is still reported. If they are still there after 3 minutes the
transaction is presumed dropped and the next tick rebuilds from a fresh
snapshot. Same rule as the miner's treasury; one transaction in flight at a
time, which is what keeps the process from double-spending its own inputs.

### 2.6 Timing

Tick every 10 seconds. The transaction is stamped at the current slot and
pushed past the newest consumed input by the transaction pace, as
`MakeCompactTransaction` already does; the per-sender pace gate drops
transactions stamped too close together silently, and one transaction per
tick with a 10 second tick keeps clear of it.

Every node call is retried with exponential backoff, as in the miner: a
wallet process must ride out node restarts and API timeouts. A deterministic rejection of a built transaction is not
retried; it is logged with the transaction shown and the tick ends.

## 3. Configuration

New profile section, all keys optional, with a flag overriding each:

```yaml
consolidate:
    # act once the consolidatable balance exceeds this, in PROX
    threshold_prox: 1000
    # balance always kept in the wallet on plain sigLock outputs, in PROX
    minimum_balance_prox: 100
    # most outputs one consolidating transaction consumes
    max_inputs: 30
    # compact whenever this many consolidatable outputs have piled up, even
    # below the threshold
    compact_at: 10
    # 'own' sends everything above the minimum to wallet.sequencer_id, which
    # must be controlled by this wallet; a sequencer ID sends it to that
    # sequencer; empty leaves the tokens in the wallet (see autodelegate)
    send_to_sequencer: own
    # applies only when send_to_sequencer is empty: 'random' delegates to a
    # sequencer drawn at random among the active ones on every action, a
    # sequencer ID always delegates to that one, empty only compacts
    autodelegate: random
    # number of own delegations to build up to; beyond it existing ones are
    # topped up, and extra ones are folded together
    target_delegations: 5
    # size a delegation is grown to before the next one is created, in PROX
    target_delegation_prox: 10000
```

| Key | Flag | Default | Notes |
|-----|------|---------|-------|
| `consolidate.threshold_prox` | `--threshold-prox` | 1000 | PROX. The balance trigger of §2.2; must be at least the minimum. |
| `consolidate.minimum_balance_prox` | `--minimum-balance-prox` | 100 | PROX, not motes, and the name says so: this is a user-facing floor, and every other proxi amount is in motes. |
| `consolidate.max_inputs` | `--max-inputs` | 30 | 2..256. |
| `consolidate.compact_at` | `--compact-at` | 10 | The second trigger of §2.2. |
| `consolidate.send_to_sequencer` | `--send-to-sequencer` | empty | `own`, a sequencer ID, or empty. |
| `consolidate.autodelegate` | `--autodelegate` | empty | `random`, a sequencer ID, or empty. |
| `consolidate.target_delegations` | `--target-delegations` | 5 | `max_delegations` / `--max-delegations`, the earlier name, is read when this one is not set. |
| `consolidate.target_delegation_prox` | `--target-delegation-prox` | 10000 | PROX. |

Also read, not new: `wallet.sequencer_id` (for `own`), `tag_along.*` (fee and
fee target). `delegate.minimum_cut` is not read: the wallet is a price taker.

The wallet profile template gains the section, commented, with
`send_to_sequencer` and `autodelegate` left empty: the template cannot know
whether the wallet controls a sequencer, and a wallet that consolidates into
one output is still better than one that does not run the command.

## 4. Output to the user

All on stdout through `glb.Infof`, as every proxi command. At startup, a
banner with the effective configuration: account, minimum, input cap, the
mode in force and its target, and the tag-along sequencer and fee. Then one
line per event:

- an action: what was consumed (count, total), what moved and where, what was
  kept, the transaction ID, and that it is submitted and not awaited;
- an action deferred: which mode was skipped and why (target inactive, target
  not controlled by the wallet, amount below the minimum inflatable, no
  sequencer leaves the required cut, in flight);
- a failure: the node error, and that the tick will retry;
- a settled or timed-out transaction.

A quiet tick prints nothing. `-v` adds the per-output classification and the
retry chatter.

## 5. `proxi node mine`, and the miner in general

The consolidator and the miner are separate processes. The miner can be
anyone's program, and the consolidator assumes nothing about its behaviour:
it only ever sees the account. `proxi node mine` is one such miner and it is
**not changed** in this step: its treasury loop keeps running for whoever
still relies on it, and the consolidate command is written without touching
or sharing any of the miner's files, so the two can be tested side by side.

The retirement is a later, separate step, after consolidate has been
committed to `develop` and tested: delete the treasury loop and everything
only it used (`mine_treasury.go`, `mine_topup.go`, `mine_treasury_test.go`,
the `held`/`heldCount` fields and the `compactions`/`delegations` counters of
the totals line, and the flags listed in the status block), fold the
duplicated helpers (delegation target selection, `retryCall`) onto the
consolidate versions, and drop the `proxi node compact auto` stub. The miner
then keeps mining only, and its banner and the docs site tell the operator to
run `proxi node consolidate` alongside it, on the same profile.

## 6. Documents to follow

After the code, one at a time, on the docs site: a `participate/consolidate.md`
page; the mining page loses its treasury section and points at it;
`wallet_config.md` gains the section; `proxi.md` lists the command. In this
repo: `kb/compact.md` records that auto mode shipped as consolidate; the
`kb/` index in `CLAUDE.md` and the `ARCHITECTURE.md` index get this document.

## 7. Decisions taken at approval

1. The second trigger by output count (§2.2): yes, `compact_at`, default 10.
   Revised the same day: the balance trigger is a separate `threshold_prox`
   (default 1000 PROX) rather than twice the minimum, and it needs at least
   two outputs, since one large output is not scattered.
2. The minimum in PROX, and the key named for it: `minimum_balance_prox`.
3. A remainder too small to be an output is folded into `moved` (§2.3); "too
   small" is the sigLock storage deposit, not one PROX (review of 2026-09-15).
4. The miner and the consolidator are separate processes. The miner can be
   any program, and nothing here assumes its behaviour; `proxi node mine` is
   left as it is until consolidate has been tested (§5).
