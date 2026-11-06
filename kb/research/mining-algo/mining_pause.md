# Mining paused until the next library upgrade (stop-gap, not planned)

> **NOT PLANNED, kept in reserve (2026-11-06).** Was library upgrade 6.3, written while the choice
> between A, B and C was open. The choice is made (C, `kb/library_upgrade6_mining.md`), and that
> upgrade ships without a pause. This stays here in case mining must stop before it does.
>
> Was: SPEC, NOT BUILT. Branch `feat/upgrade6-3` (2026-11-05). A stop-gap, not a fourth option for the
> mine chain: this upgrade stops mining until the chosen one ships. Sixth live library upgrade, no reset.
> Prerequisite: upgrade 5 active (slot 153000). Mechanism: `ledger/upgrade.md`. If listed, it takes
> index 6, and the chosen mining option ships as upgrade 7, which also ends the pause.

## 1. Why pause

The weak-key speedup and the private inclusion through an own sequencer (options A1, A2) are in use
today, and every slot pays them 95 PROX. Each of the three remedies needs a new miner, a new seeker
and a difficulty decision, and two of them are not built. Meanwhile the emission keeps going to
whoever exploits the current rules, and the reason to run a sequencer just to mine keeps adding
sequencers.

A pause is the cheapest way to stop that: no new work function, no new payout, one constant, and
nothing about the choice is decided by it.

## 2. The rule

From the activation slot, no mine transit is valid. The upgrade library redefines one constant:

```
// Library upgrade 6.3 (kb/research/mining-algo/mining_pause.md).
//
// Replaced: mining is paused. The mine lock refuses every transit stamped before
// this slot (_mineShape), so the largest slot a ledger time can carry closes the
// mine chain until a later upgrade sets the constant back. Nothing else changes:
// the mine output stays where it is, with its R, B, C, V and O.
func constDisableMiningUntilSlot : u64/4294967295
```

The check it drives is already in `_mineShape` (as replaced by upgrade 1):

```
require(lessOrEqualThan(uint8Bytes(constDisableMiningUntilSlot), uint8Bytes(txSlot)), !!!mining_is_disabled_until_the_start_slot)
```

The value is `math.MaxUint32`, the largest slot a `base.LedgerTime` holds, so no transit can ever
satisfy it; the Go side reads the constant as `uint32` (`ledger/constants.go`), so a larger value
would be truncated.

Upgrades 1, 2 and 5 already replaced or added constants this way. No other function changes, the
miner version stays 5, and nothing is refused that the ledger accepts outside the mine chain.

### 2.1 What stays as it is

| | During the pause |
|---|---|
| The mine output | unspent, R, B, C, V, O unchanged: 50 PROX on the output, R about 927.2 M PROX, B 37, C 7, V 5 (LRB at slot 155088) |
| A transit stamped before the slot | valid, settled as today |
| Branch inflation, chain inflation | unchanged: the pause removes the mined part only: 95 PROX a transit, about 670,000 PROX a day at the observed pace of 1.19 slots a transit |
| Delegations, top-ups, tag-alongs, sequencers | unchanged; sequencers lose the 1 PROX inclusion fee per slot |
| Mined outputs already held | ordinary outputs, untouched |

## 3. Ending the pause

The next library upgrade replaces `constDisableMiningUntilSlot` again, with a slot at or after its
own activation slot, together with whichever mining option is chosen and its miner version. Setting
it a little after the activation slot (a few hundred slots) gives miners a quiet start to move to the
new release, as at genesis.

### 3.1 What the first transit after the pause sees

The retarget reads only the gap M from the predecessor's slot. The first transit after the pause has
M in the tens of thousands, so:

- the required difficulty `K = max(B - (M - P), E)` is the floor E = 10;
- the successor's B is eased to E (`_mineEased`); C is kept.

From there B climbs one bit per 8 full slots (`constMineHardenAfter`). From 10 back to today's 37 that
is 27 x 8 = 216 slots, about 37 minutes, during which almost any hardware solves a slot. That does not
change the emission: the pace is one transit per slot whatever the difficulty, and several solutions
in a slot are settled by the canonical winner rule (`kb/mine_conflict_rule.md`), whose draw still
follows hashrate. Under options B and C the work function changes anyway and their own difficulty
plan applies (`kb/research/mining-algo/option_b_key_hash.md`, section 6.1).

### 3.2 The emission schedule does not wait

A is a function of the slot (`_mineAmountAtSlot`): flat 95 PROX up to slot 506,250, then rising by 134
motes a slot. The pause does not move that slot, so every paused slot is a slot of the flat phase not
mined. R is not spent during the pause, so nothing is lost from the total: the chain runs that much
longer at the end, at a larger A. If the flat phase should keep its length, the resuming upgrade can
move `constMineRampStartSlot` by the length of the pause; that is its decision, not this one's.

## 4. Consequences in the code

The ledger change is the one line above, in `ledger/def/upgrade6_3.easyfl` built by a new
`BuildUpgrade6_3Library` in the pattern of `ledger/def_upgrade5.go`, listed in `ledger.Upgrades` once
the slot is chosen. The node and the miner already honour the constant per slot, but read it as a
start slot, and three places do not survive a value that never comes:

| Where | Today | Change |
|-------|-------|--------|
| `ledger` | | a named constant, `MiningPausedSlot = math.MaxUint32`, and `Constants.MiningPaused()` on both constant types (`ledger.Constants`, `txbuildercore.Constants`); never convert the paused value to clock time: `MaxUint32` slots is about 1,400 years, past the int64 nanoseconds of `time.Time` |
| `core/core_modules/txinput_queue` | drops a transit stamped before the start slot (`mine_early_drop`) and warns once per slot that "a miner is rushing the quiet start", printing the start slot's clock time | keep the drop; when paused, log "mining is paused by the ledger" at most once per hour and print no clock time. Core: `-race` |
| `proxi/node_cmd/mine` | reads the start slot once at launch (`m.consts`); `awaitMiningOpen` sleeps until it, `opensAt` prints its clock time | the start slot of the slot being built for (`constsAt(succSlot)`), so a miner running across the activation slot stops instead of grinding transits nobody accepts; when paused, say so, print no clock time, and poll the constants of a slot ahead (they include a listed upgrade) every few minutes, resuming when a later upgrade reopens mining |
| `ledger/constants.go` | prints "Mining disabled until slot: N" | "Mining paused until a later library upgrade" when paused |
| `api`, wallet | serve and parse the constant | nothing: `disable_mining_until_slot` carries the value as is |

### 4.1 The monitor's fair-launch section

The `/monitor` page (`api/monitor/monitor.go`, drawn by `renderFairLaunch` in
`api/monitor/monitor.html`) knows nothing of a pause. It reads the mine output's R and B from the LRB
state, walks the mine chain back through the txstore and projects the emission forward. During a
pause each tile shows:

| Tile | What it shows during a pause | Correct? |
|------|------------------------------|----------|
| Bootstrap capital, its share, Mined, Idle mined capital share | read off the state and the census; mined frozen, bootstrap capital still growing | yes |
| PoW difficulty B | the last carried B (37), "confirmed" | stale, harmless |
| Pace | the mean slot gap of the last 32 transits, which a pause never changes (1.19 on 2026-11-05); "0 mined in the last hour"; "last transit ... ago", marked as a warning from 4 slots on, forever | misleading: a mean pace from before the pause next to a warning that looks like a stall |
| Mining rewards | "Per transit: 95 PROX, Per day: about 670,000 PROX", from the stale mean pace | **wrong**: nothing is minted |
| Decentralization milestones | dated crossings of 1x and 2x the bootstrap capital, from the stale pace | **wrong**: they are on hold |
| All mined | a completion date and the transits left, from the stale pace | **wrong** |
| Competing miners | empties after 120 slots: the ingress gate drops a paused transit before the mining-stream event the monitor listens to, so "no transit seen on the stream yet" | yes |
| Difficulty chart and rewards fold, transits fold | the history windows are anchored on the LRB slot, so they empty after 6 hours; the transits fold keeps the last 32 transits from before the pause | yes |

The fix is small and belongs to this upgrade:

- **Server** (`fairLaunchLive`): `paused` and `paused_from_slot`, taken from the library of the LRB
  slot (`ledger.L(lrbSlot)`), not from the newest one, `ledger.L(base.MaxSlot)`, which the monitor
  uses for everything else and which carries a listed upgrade ahead of its slot. When the newest
  library pauses and the LRB slot's does not, report `pauses_at_slot`, the activation slot of the
  listed upgrade, so the page announces the pause ahead of time.
- **Page**: when `paused`, the Mining rewards tile reads "paused since slot N, until a later library
  upgrade" with 0 per day; Decentralization milestones and All mined read "on hold while mining is
  paused"; the Pace tile drops the stall warning and says "paused". When `pauses_at_slot` is set,
  the Mining rewards tile adds "mining pauses at slot N, date". Never convert the paused value to a
  date (section 4).

Nothing in `sequencer/mine`: no transit reaches it. A miner on an older release keeps submitting
transits that every node drops at ingress, as it would have before the start slot; harmless, and the
counter `mine_early_drop` shows it.

## 5. Downsides

- **Miners earn nothing until the next upgrade.** Those mining honestly lose with the rest; this is
  the price of stopping the exploit now rather than later. Announce the slot well ahead, with the
  reason and the plan to resume.
- **No emission is a fairness choice too.** Mined supply is 12.78 M PROX (17.4% of supply). The pause
  freezes that distribution, the exploiters' share included, until mining resumes.
- **The pause is open-ended by design.** Its end is a release, not a slot written now. If the chosen
  option takes long, the pause takes long.
- **The restart is cheap for a while** (3.1): about 37 minutes of near-floor difficulty under the
  current work function.

## 6. Tests

`ledger/tests/library_upgrade6_3_test.go`:

- a valid transit one slot before the activation slot is accepted, the same transit stamped at the
  activation slot is refused with `mining_is_disabled_until_the_start_slot`;
- a test library with a later upgrade setting the constant back: the first transit after the gap is
  valid at the floor difficulty, its successor carries B = E and the predecessor's C;
- the constants of a slot from the activation slot report `MiningPaused()`, and the start slot reads
  back as `math.MaxUint32`, not truncated.

`core/core_modules/txinput_queue`: a paused-slot transit is dropped without clock-time conversion
(`-race`). `proxi/node_cmd/mine`: `successorSlot` and the wait logic with a paused slot ahead, and a
miner whose launch constants are open but whose target slot is paused.
`api/monitor/monitor_test.go`: `paused` and `paused_from_slot` from the LRB slot's library,
`pauses_at_slot` while the pause is listed but not active, and no clock time for the paused value.

## 7. Open

- The activation slot, and how far ahead to announce it.
- Whether the resuming upgrade moves `constMineRampStartSlot` by the length of the pause (3.2).
- Whether the resuming upgrade keeps the floor restart (3.1) or sets B on the first transit; the
  latter needs a rule for the first transit, the former needs nothing.
