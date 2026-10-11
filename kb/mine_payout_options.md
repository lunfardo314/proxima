# Mine payout options: the covenant prices what a miner does with the reward

> **SPEC, not built** — written 2026-10-11 against the reset testnet of
> 2026-10-10, where one operator mined with a fresh wallet per restart and never
> swept: 250 payouts, about 23,500 PROX, idle on eleven abandoned wallets in
> three hours. Touches the mine covenant, the miner and one ingate rule; not
> the core. A ledger change, so it ships with the next reset. **Not a
> replacement for `kb/forced_delegation.md`**, which handles dead capital of
> any origin after the fact; this one makes dead capital expensive to mine in
> the first place. **Built on `develop` 2026-10-11**, with the ledger change
> waiting for the next reset. Forced delegation is the complete answer but carries high
> complexity and behavioral consequences nobody has seen; this is the simple
> one, and therefore the one to build first. It may be enough for the
> bootstrap phase, until the global equilibrium of a priced token makes idle
> capital its holder's own loss. `proxi node consolidate` stays as it is for
> every other wallet. **Amounts revised 2026-10-11 for the next reset**, after
> a day of watching the running ledger (20 and 80 PROX): the opening goes to
> **15 PROX** and the new delegation to **50 PROX**, both account openers
> priced alike, the delegation held up by the ledger's two floors. The code
> still carries 20 and 80 until that change is built.

## 1. Why

Today a transit pays 94 PROX to a plain sigLock output of the miner and 1 PROX
to the including sequencer, whatever the miner does afterwards. Sweeping the
payouts into delegations is the miner's job, by the consolidator the miner
runs beside itself since v0.11.4. A miner that runs without one, by neglect, by
a profile it cannot run on, or by a wallet it throws away on every restart,
leaves every payout idle, and idle capital is a hole in the health margin
(`kb/forced_delegation.md`, §1). The embedded consolidator cannot reach such a
miner, and the miner version rule cannot either, since the miner is current.

The reward is the one thing every miner wants, and the covenant decides its
shape. So let the covenant pay **less for a payout that lands idle and the
full amount for one that lands in consensus**. The miner then does the right
thing because it is the profitable thing, with no second process and no
goodwill.

## 2. The three payment options

The mine transit keeps its successor at index 0 and the reward outputs after
it, and the covenant accepts exactly one of three reward layouts, each an
exact shape: no other output may be added, and every layout carries exactly
one tag-along. A is the schedule amount, 95 PROX in the flat phase, and T the
fixed inclusion fee, 1 PROX. A stays at 95: the schedule test pins it, since A
times the flat phase at the realised pace is what carries mined supply to the
7/12 point exactly at the ramp start. The option a transit takes is written into the successor's mine
lock as its fifth argument (§3), so the covenant, the node's ingate and every
reader of the transit see it without inferring it from the layout.

| Option | Reward outputs | Minted | Who it is for |
|---|---|---|---|
| 1, **opening** | `[1]` sigLock to the signer, `constMineOpeningAmountProx` = **15 PROX**; `[2]` tag-along T | 16 PROX | A miner with no account yet. Pocket money for fees and for being a known sender. |
| 2, **new delegation** | `[1]` delegation origin, master = the signer, any target, any cut, `constMineDelegationAmountProx` = **50 PROX**; `[2]` tag-along T | 51 PROX | A miner that wants one more delegation. |
| 3, **full** | `[1]` one tag-along of **A**, sender = the signer, any request payload or none | 95 PROX | A top-up of an existing delegation (`ensureTopUpDelegation`), a transfer to the miner's own sequencer, or any other request. |

The amounts of options 1 and 2 are absolute constants stated in PROX, the
`Prox` suffix saying so, not shares of A: they stay where they are when A
moves along the schedule. The covenant compares in motes,
`mul(constMineOpeningAmountProx, constMotesPerProx)`, the unit constant
being new to the library unless it already carries one (the Go side has
`base.PROX`). The successor's
inflation and the decrement of R equal the minted amount of the option taken,
not a fixed A. A miner that takes option 1 leaves 79 PROX of that slot's
emission unminted, for good: the schedule is a ceiling, not a promise.

**Cost per token.** Electricity per transit is the same whatever the option,
so a token costs 6.3 times more under option 1 than under option 3 and 1.9
times more under option 2. A miner that wants tokens on plain outputs pays
six times for them; a miner that wants a new delegation every time pays
nearly double what a miner that tops up pays. Both are allowed, both are
priced.

**Fifteen for the opening, fifty for the delegation.** Both options are
account openers, one for the wallet and one for a delegation, and the first
day of the running ledger (20 and 80 PROX) showed the pricing works: every
miner moved to top-ups as soon as it could, and the one that did not paid
for it. So both are set as low as the ledger allows. The storage deposit of a
plain sigLock output is 9.25 PROX, so 15 PROX leaves 5.75 PROX of spendable
float, enough for dozens of wallet transactions at the fees sequencers ask,
and a miner's own transits mint their inclusion fee. The delegation has two
floors: the storage deposit of a delegation output, 135 to 137 bytes with
two index values, is about 46 PROX, and the minimum inflatable amount, the
target base supply over the slot inflation base, is 30.3 PROX, under which a
chain earns nothing. Fifty clears both with a small margin; fifteen, as first
considered, would not validate. At 15 the cost ratio of the opening against
the full option is 6.3 and an attacker at half the hashrate puts about
58,000 PROX a day idle against the 43 million the health rule bears today,
two years. The float is not the binding constraint, the disincentive is.

**Why the targets are safe.** A transit is in the ledger only when a
sequencer includes it, and a sequencer includes it by consuming its
tag-along. A tag-along to a target that never consumes it means the transit
is never included: it is orphaned, not idle, and there is nothing to clean up.
So "any target" on option 3 means in practice the sequencer that will include
the transit, which is the one the miner tags along with today. A delegation
under option 2 whose target never freezes it, or whose key is thrown away, is
idle capital of the delegation kind, as a sigLock output is of the plain
kind; that is why the option costs more than a top-up, only less than the
opening, since a frozen delegation is in coverage and
`kb/forced_delegation.md` takes over one that stays unfrozen. The only
layout that produces idle capital by construction is option 1, at 15 PROX per
transit and, by the ingate rule below, once per account in practice.

## 3. The covenant

The mine lock gains a fifth argument, `O`, the **payout option of the transit
that produced this output**: 1, 2 or 3, and 0 on the genesis mine output.
`mineLock(R, B, C, V, O)`. The consumed arm reads the successor's `O` and
requires the matching reward layout; nothing is inferred from the output
count or the lock kinds, and every reader of a transit, the node's ingate, the
miner's verifier, the monitor, takes the option off the successor's lock
bytes with the same one-level parse they use for R, B, C and V.

`_minePayoutAndFee` in `ledger/def/lock_mine.easyfl` becomes `_minePayout`,
dispatching on `_mineSuccArg(4)`. Every arm pins the signer as the
beneficiary, which is what keeps mining non-outsourceable: the payout
holder, the delegation master or the tag-along sender is
`txHolderID(txSignatureData)`.

```
func _mineOpening :
and(
   require(equalUint(numProducedOutputs, u64/3), !!!mine_opening_needs_three_outputs),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #sigLock), !!!mine_opening_payout_must_be_sig_locked),
   require(equal(_mineOutIndexValue(1, 0), txHolderID(txSignatureData)), !!!mine_payout_must_go_to_the_transaction_signer),
   require(equalUint(_mineOutAmount(1), _prox(constMineOpeningAmountProx)), !!!mine_opening_amount_must_be_the_opening_amount),
   _mineInclusionFee(2)
)

func _mineNewDelegation :
and(
   require(equalUint(numProducedOutputs, u64/3), !!!mine_delegation_needs_three_outputs),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #delegateLock), !!!mine_delegation_payout_must_be_a_delegation),
   require(isChainOriginID(parseInlineDataArgument(_mineOutChain(1), 0, #chain)), !!!mine_delegation_must_be_an_origin),
   require(equal(_mineOutIndexValue(1, 0), txHolderID(txSignatureData)), !!!mine_delegation_master_must_be_the_transaction_signer),
   require(equalUint(_mineOutAmount(1), _prox(constMineDelegationAmountProx)), !!!mine_delegation_amount_must_be_the_delegation_amount),
   _mineInclusionFee(2)
)

func _mineFull :
and(
   require(equalUint(numProducedOutputs, u64/2), !!!mine_full_payout_needs_two_outputs),
   require(equal(parseBytecode(_mineOutLock(1), 0x), #tagAlong), !!!mine_full_payout_must_be_a_tag_along),
   require(equal(_mineOutIndexValue(1, 0), txHolderID(txSignatureData)), !!!mine_tag_along_sender_must_be_the_transaction_signer),
   require(equalUint(_mineOutAmount(1), _mineAmount), !!!mine_full_payout_must_be_A)
)

// $0 an amount in PROX -> motes
func _prox : mul($0, constMotesPerProx)

// $0 output index of the inclusion fee
func _mineInclusionFee :
and(
   require(equal(parseBytecode(_mineOutLock($0), 0x), #tagAlong), !!!mine_inclusion_fee_must_be_a_tag_along),
   require(equalUint(_mineOutAmount($0), constMineTagAlongFee), !!!mine_inclusion_fee_must_be_the_fixed_amount)
)

// the option the successor declares selects the layout
func _minePayout :
or(
   and(equalUint(_mineSuccArg(4), u64/1), _mineOpening),
   and(equalUint(_mineSuccArg(4), u64/2), _mineNewDelegation),
   and(equalUint(_mineSuccArg(4), u64/3), _mineFull),
   require(false, !!!mine_successor_must_declare_a_payout_option_1_2_or_3)
)

// the amount this transit mints: the option's reward plus, for the first two
// options, the inclusion fee
func _mineMinted :
if(equalUint(_mineSuccArg(4), u64/3),
   _mineAmount,
   add(_mineOutAmount(1), constMineTagAlongFee))
```

The output-count rules inside the arms make each layout exact: three outputs
for options 1 and 2, two for option 3, exactly one tag-along in each.
`_mineShape` keeps its guards and drops the fixed "three outputs" rule to the
arms, `_mineSuccessorState` takes `_mineMinted` where it took `_mineAmount`
for the inflation and the R decrement, and the exhaustion check compares R
with the minted amount. `_mineOutIndexValue`, `_mineOutAmount` and
`_mineOutChain` are readers of a produced output by index, in the style of the
existing `_mineOutLock`. The tag-along's request payload at element 3 and an
ensure constraint at element 4 are not the covenant's business: any request,
or none, is accepted, and the target sequencer judges it as it judges any
request.

The delegation of option 2 is validated by `delegateLock`'s own produced arm
as every delegation origin is: index values `[master, target]`, state
`undef`, the cut as the miner set it. Its target's existence is not checked,
as it is not for a voluntary delegation.

## 4. The one ingate rule

Today a mining transaction is exempt from the unknown-sender rule, since a
fresh miner's holder owns nothing yet. Under this spec the exemption narrows
to **option 1**: a mining transaction whose reward output is a plain sigLock
passes the rule, any other mining transaction must come from a holder known
in the latest reliable branch, like every other transaction. The ingate reads
the option off the successor's lock argument, the same one-level parse the
proof-of-work floor already does on the transit, so the check costs nothing
new. That is a one-line change in `checkSenderPace`
(`core/core_modules/txinput_queue`), node policy, not consensus, and it is
what makes option 1 the once-per-account choice: a miner that keeps throwing
its key away can only ever take the 15 PROX option, since its new holder is
never known. Its payouts cost it six times over, and it never gets a
delegation or a top-up.

## 5. The miner

`proxi node mine` chooses the option per round, before building the transit,
from the account it reads off the node. No consolidator runs beside it any
more for the payouts; `--disable_consolidation` and `mine.consolidate` go. A
miner that wants the wallet tidied for other reasons runs
`proxi node consolidate`, which is unchanged.

The choice, in order, with the same settings the consolidator uses
(`consolidate.target_delegations`, `consolidate.target_delegation_prox`,
`consolidate.send_to_sequencer`):

1. **Holder not known in the reliable branch** (`is_known_controller`): option
   1, opening. The tag-along target is the inclusion sequencer by rating, as
   today. Taken once; the account is known from the first confirmed transit.
2. **`send_to_sequencer` set** and that sequencer controlled by this wallet and
   active: option 3, a plain tag-along of A to it. The sequencer takes it
   as it takes any fee.
   Every option's tag-along must target an **active** sequencer, checked by
   the miner against the sequencer list as it checks its tag-along target
   today: a transit whose tag-along nobody consumes is never included.
3. **An own delegation below `target_delegation_prox`** whose target is
   active and asks at most A per top-up: option 3, a top-up request of A
   (`ensureTopUpDelegation`) to the smallest such delegation. The floor of the
   minimum top-up is 100 PROX and A is 95 in the flat phase, so the floor
   must come down to A or below for mine top-ups to be taken at all: it moves
   to 90 PROX with this spec.
4. **Fewer own delegations than `target_delegations`**: option 2, a 50 PROX
   delegation to a target drawn by the delegation rating at the cut it leaves,
   exactly `selectDelegationTarget`.
5. **Otherwise** option 3, a top-up of the smallest own delegation that can
   take one; and if none can, because every target is inactive or priced above
   A, option 2 to a fresh target; and if no target leaves anything, option 1
   rather than nothing.

The placement rules are the consolidator's (`kb/consolidate.md`, §2.4b):
grow one delegation to the target size, then the count to the target, then top
up the smallest. The target draw, `SelectDelegationTarget`, is exported from
package `consolidator` and reused; the ordering is a few lines the miner
carries itself. The miner does not fold or re-delegate, that stays the
consolidator's work for a wallet that chooses to run it.

The miner reads its delegations once per round like the consolidator does
(`GetOutputsForControllerID`, delegate-master lock type) and the sequencer
list once per round (it does already, for the tag-along). To that it adds
what its own unconfirmed transits did (`speculativePayouts` of the mine tree,
merged by `withSpeculative`): a delegation opened by a transit still in flight
counts, under the chain ID of its origin output, with the amount a new one
holds, and a top-up in flight is credited to the delegation it names. The
reliable branch settles a transit 5 to 7 slots after it is submitted, so
without this a miner winning every slot in between opened one delegation per
slot until the first showed up (seven in seven slots, three times, on the
2026-10-11 reset) and sent every top-up to the same "smallest" one. A transit
that loses its slot is dropped from the tree, and its entry with it. A top-up
to a pending delegation extends the lineage that opens it, so its target has
the delegation by the time it takes the request. The opening is not treated
this way: whether the holder is known is the node's reading of its own
reliable branch, which is what the ingate applies.

The transit verifier (`mine_verify.go`) and the mine tree accept the three
layouts and read the minted amount off the transit, as the covenant does.

## 6. What it changes in the economy

- **Emission** becomes at most A per slot and in practice a little less: a
  new miner's first transit and every new delegation mint less than A, so the
  schedule's 49 million over the flat phase is a ceiling. With the observed
  pace of about 1.1 slots per transit, the flat phase mints about 7,850
  transits a day, up to 746,000 PROX.
- **Dust mining** of plain outputs costs six times more per token and is
  bounded by the ingate to new accounts: at 15 PROX per transit and a new
  account per transit, an attacker holding half the hashrate puts about
  58,000 PROX a day idle, against the 43 million the health rule can bear
  today. Two years, not two months.
- **Delegation count** is bounded by price: each extra delegation costs a
  miner 45 PROX of forgone reward, so the target count is what honest miners
  settle at, and an attacker who creates one per transit mints about half
  and hands every one of them to a sequencer's coverage, or leaves them idle
  of the delegation kind, which the forced delegation spec takes over.
- **Own sequencers** get the full reward, like a top-up. A miner that runs a
  sequencer is not penalised for feeding it; it gains the sequencer's cut on
  others' delegations and the branch bonus on top. That is the rule 4 lever,
  see §7.
- **Nothing to sweep** for a `proxi` miner. The wallet holds its 15 PROX of
  pocket money and its delegations; the top-up requests are consumed by the
  targets within a slot or become reclaimable.

What it does **not** do: stop a miner that keeps its keys from withdrawing its
delegations later and idling the tokens. That is dead capital of any origin,
and it is `kb/forced_delegation.md`'s job. The two are complements, not
alternatives: this one makes the hole expensive to dig, that one fills it.
The order is this one first. Forced delegation overrides a lock without its
holder's signature, needs turns and takeovers, and has behavioural
consequences nobody has observed, for holders, for sequencers competing over
dormant outputs, for wallets that come back to life. This spec changes only
what a miner is paid for, with no new path into anyone's output, and it
bounds the hole to a leak that takes a year and a half to matter at half the
hashrate. That may be all the bootstrap phase needs: once the token has a
price, idle capital costs its holder dilution, and the global equilibrium
does what the covenant does now. Forced delegation stays specified for the
case where it does not.

## 7. Decentralization milestones

The milestones that matter are the bootstrap capital's share of the total
supply, since the health rule is `7/12` of the supply and the bootstrap
capital is what commits healthy branches alone today. Mined supply M is what
moves it: with B = 60 million the bootstrap capital, the share B / (B + M)
reaches a fraction s when M = B (1/s − 1).

The emission, at the observed pace of 1.1 slots per transit (7,850 transits a
day) and every transit on the full option: 746,000 PROX a day for the 60 days
of the flat phase, 44.7 million in all, then A growing by 134 motes per slot,
which adds about 1.16 PROX per transit per day, so the daily emission is
746,000 plus 9,100 per day past day 60. The schedule is unchanged by this
spec, so these are today's durations too; the options only make them
ceilings.

| Bootstrap share falls to | Mined supply needed | Day | What it means |
|---|---|---|---|
| 7/12 | 42.9 million | about 57 | the bootstrap capital alone can no longer commit a healthy branch; from here the network lives on community capital. Inside the flat phase, as the covenant's own comment intends and the schedule test pins. |
| 1/2 | 60 million | about 78 | mined supply equals the bootstrap capital |
| 5/12 | 84 million | about 102 | the community alone clears the health rule with the bootstrap capital idle |
| 1/3 | 120 million | about 131 | the bootstrap capital could not keep two forks alive even with half the community |
| all mined | 940 million, the mine chain's R | about 429, fourteen months | the mine chain is exhausted at a total supply of 1 billion; the bootstrap share is 6 percent. The last day mints about 4.1 million at an A of about 520 PROX. |

Sensitivities, all small: a pace of 1.0 instead of 1.1 brings every day about
10 percent earlier; a realistic option mix, one opening per miner and a few
new delegations, mints a few percent less and delays each by a day or two;
chain inflation on delegated capital, about 6 million PROX a year network
wide, grows the bootstrap side too and delays the later rows by a few days.
Mined-share rows assume mined tokens end up in consensus, which is what the
options are priced for; the share of supply is unaffected by where the tokens
sit, only the health margin is.

These are durations the schedule sets, not the published reset rules, which
are about the shape of what the community does with the capital; the two
specs' effect on those rules is one line: idle mined capital goes
structurally near zero, and running an own sequencer earns the same as
delegating plus the sequencer's cut and the branch bonus.

## 8. Constants

| Constant | Default | Note |
|---|---|---|
| `constMineAmountBase` | 95 PROX | unchanged; the schedule test pins it to the 7/12 point |
| `constMineOpeningAmountProx` | 15 (20 on the running ledger) | in PROX; 5.75 PROX above the 9.25 PROX storage floor; cost per token 6.3 times the full option |
| `constMineDelegationAmountProx` | 50 (80 on the running ledger) | in PROX; above the delegation output's storage deposit of about 46 PROX and the 30.3 PROX minimum inflatable amount; cost per token 1.9 times the full option |
| `constMotesPerProx` | 1,000,000 | the unit, so the two above read in PROX |
| `constMineTagAlongFee` | 1 PROX | unchanged; options 1 and 2 only |

Option 3 mints A, no separate fee: the tag-along is the whole reward and the
target's payment at once. The difficulty rules are untouched and the same for
every option: the whole point is the reward.

## 9. What changes where

- `ledger/def/lock_mine.easyfl`: the fifth lock argument `O`, `_minePayout`
  with the three arms, `_mineMinted`, the readers; header comment.
- `ledger/def/def_constants0.json` and the Go mirrors: the two amounts in
  PROX (`MineOpeningAmountProx`, `MineDelegationAmountProx`), the unit
  constant; A stays at 95 PROX.
- `sequencer/seqdata` and `txbuildercore.MinimumTopUpAmount`: the top-up floor
  from 100 to 90 PROX, so a full-option top-up of A is never under it.
- `ledger/lock_mine.go`, `ledger/txbuildercore/helpers_mine.go`:
  `MineLock.O` and `MineLockView.O`, five-argument builders and parsers, the
  genesis mine output at `O = 0`; `MineMintedAmount` off a transit's successor
  and reward output; the three layouts as builder helpers.
- `core/core_modules/txinput_queue`: the exemption narrowed to option 1 (§4).
- `proxi/node_cmd/mine`: the choice of §5 (`mine_payout.go`), the three
  transit layouts in `buildTransit`, the verifier and the tree reading the
  minted amount, the round's log line saying which option and why, the
  consolidation flag and key removed.
- `consolidator`: `SelectDelegationTarget` exported; nothing else.
- `api/monitor`, `api/chain_explorer`: the minted amount per transit read off
  the transit, the fair-launch section showing the option mix.
- Tests: `ledger/tests/mine_test.go` for the three layouts and every rejected
  deviation (wrong amount, wrong holder, wrong master, three outputs with a
  tag-along reward, two outputs with a sigLock); the ingate test for the
  narrowed exemption; the miner's unit tests for the choice.
- Docs: the site's mining and fair launch pages, `kb/mine_conflict_rule.md`
  for the covenant arguments, `kb/take1_todo.md`.

## 10. Decisions taken

1. **The opening amount is 15 PROX**, an absolute constant stated in PROX,
   revised from 20 after the first day on the running ledger (§2): the float
   above the storage floor is enough, the disincentive is what the number is
   for.
2. **The delegation amount is 50 PROX**, an absolute constant stated in PROX,
   revised from 80 for the same reason and held there by the ledger's two
   floors, the delegation output's storage deposit of about 46 PROX and the
   30.3 PROX minimum inflatable amount; nearly double the price per token of a
   top-up, so a miner opens its count and no more.
3. **Option 2's target is not checked** by the covenant. A dead, unfrozen
   delegation is idle capital like a sigLock output, which is why the option
   costs more than a top-up, and forced delegation takes it over.
4. **Exactly one tag-along per transit**, in every option; the miner checks
   that its target is an active sequencer before building, since a transit
   whose tag-along nobody consumes is never included.
5. **Difficulty is the same for every option.** The existing rules stand; the
   option prices the reward, not the work.
6. **The option is an explicit argument of the mine lock**, so the covenant,
   the ingate and every reader take it off the successor's lock bytes rather
   than inferring it from the layout.
7. **A stays at 95 PROX.** Rounding it to 100 would move the point where the
   flat phase ends away from the 7/12 milestone the schedule is sized for,
   which its test pins; the full option mints 95 and the ratios are 4.75 and
   1.17 instead of 5 and 1.25.

