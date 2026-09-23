# Take 1: breaking changes collected on `develop-take1`

> **TODO — take 1.** The list of ledger and node changes that ship with the next
> network reset. Lives on `develop-take1` only, which is kept in sync with `develop`
> and collects every breaking change. Specs are linked where they exist; an item
> without one is a one-line decision. Operational items (keys, checksums, domain,
> announcements) are kept outside the repository.

## Ledger (hardfork)

- [x] **Ensure constraints exempt the sender's reclaim.** Done on this branch.
  `ensureStopDelegation` stepped aside only at `constTagAlongReclaimSlots`, so in the
  sender's exclusive window its consumed arm still demanded the delegation it names and
  an askstop request could not be taken back until anybody could. It now steps aside
  at `constTagAlongSlots`, the moment the target can no longer consume the output; the
  spendable classifier treats every request as a plain tag-along again
  (`isTagAlongRequest`) and the consolidator reclaims it from slot 30.
  `ledger/tests/request_reclaim_test.go` pins the boundary against the ledger. Every
  later `ensure…` constraint on a request follows the same rule.
- [x] **Top-up request**: `kb/delegation_topup.md`, built 2026-09-22. New
  `ensureTopUpDelegation` (born with the escape above), exact amount rule on the
  delegate lock's target path, continuation keeps epoch and share, sequencer request
  code 4 with a minimum top-up (`MinTopUp`, floor 100 PROX), `proxi node delegate
  topup` and the consolidator on the request path.
- [x] **1-slot mining pace**, built 2026-09-23: Part B of `kb/mine_conflict_rule.md`,
  see its "as built" list. Minimum pace 1, asymmetric retarget with the counter
  argument C and `constMineHardenAfter` = 8, `constMineTargetPace` retired, cap 56,
  Go mirrors, verifier, miner (settlement deadline, contested-slot grinding), node
  pace-gate exemption, monitor, tests.
- [x] **Emission schedule for take 1**, chosen and set in the ledger constants
  2026-09-23 (`ledger/def_constants0.go`): the opening reward set so
  that the founder's option ends on day 60, then a linear rise as today. Sized at a
  realised pace of 1.12 slots per step, the equilibrium of the asymmetric retarget
  with 8 full slots per harden; genesis 60M and mintable 940M unchanged; exhaustion
  two weeks later than today's 429 days.
  - opening: **95 PROX** per step for 60 days (ramp start slot 506,250), 0.72M PROX
    a day; mined 42.9M by day 60, so the founder's option (mined above 5/12 of
    supply) ends on day 60;
  - tail: linear, **+134 motes per slot**, from 95 to 527 PROX per step, 0.72M to
    3.97M PROX a day, exhaustion on day 443 (1.21 years);
  - milestones: bootstrap 50% day 81, founder cannot stall (7/12) day 105, bootstrap
    below 1/6 day 236, half of the mintable out day 304, below 1/12 day 366;
  - sensitivity: every slot full runs 12% faster (option ends day 54);
  - covenant: today's shape, base amount, ramp start and per-slot slope, only the
    values change; supersedes the reward scaling in `kb/mine_conflict_rule.md` Part B;
  - why: no date anywhere in the schedule to time hardware or narratives to, the
    shape take 0 ran, and the longer opening buys 14 days of observation for the
    reset rules. Its cost against a flat tail is that every later milestone comes
    20 to 70 days later and the arms race peaks at the end, as today.

  The three shapes compared, all at pace 1.12 and exhaustion at ~443 days:

  | Shape | Opening | Tail | Option ends | Bootstrap under 1/6 | Half mined | Under 1/12 |
  |---|---|---|---|---|---|---|
  | Current, scaled | 125 for 46 d | linear to 475 PROX | day 46 | day 216 | day 290 | day 358 |
  | Flat tail | 95 for 60 d | flat 310 PROX | day 60 | day 170 | day 243 | day 324 |
  | **Chosen** | 95 for 60 d | linear to 527 PROX | day 60 | day 236 | day 304 | day 366 |

  The flat tail reaches every milestone earliest and ends with the smaller cliff,
  at the price of a 3.26x step on day 60 that new hardware would be timed to
  (owned hardware mines regardless). A short ramp into a flat tail is the open
  refinement if that trade is revisited.
- [x] **Tag-along fee of a mine transit exactly 1 PROX**, decided and built 2026-09-23, in place
  of today's cap at 1% of A. A fixed amount, so the fee no longer moves with the
  reward (at 95 PROX the 1% cap would be 0.95 PROX) and every transit pays the same:
  `_minePayoutAndFee` in `lock_mine.easyfl` requires the tag-along output to equal a
  new `constMineTagAlongFee` of 1 PROX, and the payout is A less that fee. A sequencer
  whose minimum fee is above 1 PROX takes no mine transits; the miner's `--fee` flag
  and its fee clamping go, since the amount is fixed by the ledger.

## Node, miner, wallet (any time before the reset)

- [x] Miner keeps a contested slot open and submits a better solution; mine
  transactions exempt from the per-sender pace gate; round deadline at the settlement
  tick (built with pace 1, 2026-09-23).
- [x] The miner's treasury loop retired (2026-09-22): `proxi node mine` only mines,
  `proxi node consolidate` on the same profile puts the payouts to work.
- [x] Consolidator defaults for the smaller, more frequent payouts (2026-09-23):
  `threshold_prox` 300 PROX (was 1000), a startup warning when the threshold less
  the minimum is under the top-up minimum; `compact_at` kept at 10.
- [ ] Stability at 30 TPS with 20 sequencers: a load run before take 1.
