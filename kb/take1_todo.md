# Take 1: breaking changes collected on `develop-take1`

> **TODO — take 1.** The list of ledger and node changes that ship with the next
> network reset. Lives on `develop-take1` only, which is kept in sync with `develop`
> and collects every breaking change. Specs are linked where they exist; an item
> without one is a one-line decision. Operational items (keys, checksums, domain,
> announcements) are kept outside the repository.

## Ledger (hardfork)

- [ ] **Ensure constraints exempt the sender's reclaim.** `ensureStopDelegation`
  steps aside only at `constTagAlongReclaimSlots`, so between the end of the
  sequencer's window and the public window its consumed arm still demands the
  delegation it names, and the sender cannot take an askstop request back until
  anybody can (`ledger/tests/request_reclaim_test.go` pins this). Fix: every
  `ensure…` constraint on a tag-along request steps aside once
  `selfInputSlotPace >= constTagAlongSlots`, the moment the target can no longer
  consume the output. Then the spendable classifier's askstop case
  (`tagAlongRequestShape` in `ledger/txbuildercore/spendable_classify.go`) collapses
  into the plain one and the request is reclaimable from slot 30 like any tag-along.
- [ ] **Top-up request**: `kb/delegation_topup.md`. New `ensureTopUpDelegation`
  (born with the escape above), exact amount rule on the delegate lock's target path,
  continuation keeps epoch and share, sequencer request code 4 with a minimum top-up
  (`MinTopUp`, floor 100 PROX).
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
- [ ] Consolidator defaults for payouts 4x smaller and more numerous; placement
  without askstop once the top-up request exists; the miner's treasury loop retired.
- [ ] Wallet classifier recognises request outputs as the sender's from slot 30 once
  the ensure escape is in (the develop version withholds askstop requests until 390).
- [ ] Stability at 30 TPS with 20 sequencers: a load run before take 1.
