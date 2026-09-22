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
- [ ] **1-slot mining pace**: Part B of `kb/mine_conflict_rule.md`. Minimum pace 1,
  asymmetric retarget with the counter argument C and `constMineHardenAfter`,
  `constMineTargetPace` retired, cap raised toward 56, Go mirrors and tests.
- [ ] **Emission schedule for take 1**, decided together with pace 1: reward scaled
  for the new mean pace; length of the flat phase; shape of the tail.
- [ ] **Tag-along fee exactly 1%** on mine transits instead of capped at 1% (?).

## Node, miner, wallet (any time before the reset)

- [ ] Miner keeps a contested slot open and submits a better solution; mine
  transactions exempt from the per-sender pace gate; round deadline at the settlement
  tick (`kb/mine_conflict_rule.md`, deferred from Part A).
- [x] The miner's treasury loop retired (2026-09-22): `proxi node mine` only mines,
  `proxi node consolidate` on the same profile puts the payouts to work.
- [ ] Consolidator defaults for payouts 4x smaller and more numerous.
- [ ] Stability at 30 TPS with 20 sequencers: a load run before take 1.
