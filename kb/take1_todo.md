# Take 1: breaking changes for the next network reset

> **TODO — take 1.** The list of ledger and node changes that ship with the next
> network reset. Collected on the branch `develop-take1` until 2026-10-08, when it
> was folded into `develop` as `v0.11.0-testnet` and deleted; everything here is on
> `develop`. Specs are linked where they exist; an item
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
- [x] **Sequencer defaults**, built 2026-10-05: every new sequencer is seeded with a cut of
  100 promille and a minimum tag-along fee of 100,000 motes (`seqdata.NewWithDefaults`);
  `proxi node seq init_genesis` applies the same unless `--fee` or `--margin` say
  otherwise. Changes the genesis output. **Bootstrap exception, 2026-10-08**: the bootstrap
  sequencer takes the cut but its minimum fee is 0, since the controller's dust output
  cannot pay 100,000 motes for the withdrawals that fund the first wallets.
- [x] **Tag-along fee of a mine transit exactly 1 PROX**, decided and built 2026-09-23, in place
  of today's cap at 1% of A. A fixed amount, so the fee no longer moves with the
  reward (at 95 PROX the 1% cap would be 0.95 PROX) and every transit pays the same:
  `_minePayoutAndFee` in `lock_mine.easyfl` requires the tag-along output to equal a
  new `constMineTagAlongFee` of 1 PROX, and the payout is A less that fee. A sequencer
  whose minimum fee is above 1 PROX takes no mine transits; the miner's `--fee` flag
  and its fee clamping go, since the amount is fixed by the ledger.

## Node, miner, wallet (any time before the reset)

- [x] **Peers on another ledger dropped and banned** (2026-10-10, node policy): after
  a reset an old node and a new one share no protocol name, so no message crosses,
  but the libp2p connection is version-blind and a leftover node stayed connected,
  alive, in a dynamic slot, failing a negotiation per gossip message. Now the first
  stream that fails as "protocol not supported" bans the peer for 10 minutes: a
  dynamic peer is dropped, a static one is warned about and closed but kept, and its
  redials are closed at connect without being registered or logged; discovery skips
  it. `peering/other_ledger.go`, test `TestOtherLedgerPeerDroppedAndBanned`;
  `core/resilience.md` ledger-version row.
- [x] **Miner version** (2026-10-10, hardfork): the ledger constant
  `constMinerVersion` (default 1) names the reference miner version it expects,
  and the mine lock gained a fourth argument V, the version of the miner that
  built the successor, which the produced arm requires equal to the constant.
  A miner on another version therefore builds only invalid transits, and the
  constant is bumped by a library upgrade at a slot whenever every miner must
  move to a new release. Two complementary signals so a miner learns why: `proxi
  node mine` carries `MinerVersion`, compares it with the ledger's at start and
  before every round and stops with "update proxi"; the mining stream handshake
  carries `miner_version` beside `ledger_hash`, refuses a mismatch with the same
  reason and closes subscribers on an old version when the constant moves. The
  miner's transit verifier rejects a streamed transit on another version. Test
  `TestMineRejectsOtherMinerVersion`; `api/api.md`, `core/resilience.md`.

- [x] Miner keeps a contested slot open and submits a better solution; mine
  transactions exempt from the per-sender pace gate; round deadline at the settlement
  tick (built with pace 1, 2026-09-23).
- [x] The miner's treasury loop retired (2026-09-22): `proxi node mine` only mines,
  `proxi node consolidate` on the same profile puts the payouts to work.
  2026-10-10: the consolidator is the repo-level package `consolidator`, and
  `proxi node mine` runs it beside the miner by default (`mine.consolidate`,
  `--disable_consolidation`), its lines prefixed `[consolidate]`, so a lazy
  miner's payouts are swept without a second process. `kb/consolidate.md` §5.
- [x] Consolidator defaults for the smaller, more frequent payouts (2026-09-23):
  `threshold_prox` 300 PROX (was 1000), a startup warning when the threshold less
  the minimum is under the top-up minimum; `compact_at` kept at 10.
- [x] Consolidator against the idle-capital goal (2026-10-05): placement falls through to
  the next delegation or a new one when the picked one cannot be reached this tick; a
  stale delegation too small to re-delegate is folded into the largest consumable one or
  ended and returned to the wallet; the wallet template's threshold corrected to 300.
  Spec: `kb/consolidate.md`.
- [x] Hands-on run of the take 1 miner on a standalone node with three throttled miners and
  consolidators (2026-10-05): pace 1, delegation creation and frozen top-ups by request
  verified end to end.
- [x] Quiet start for the mine chain: the ledger constant
  `constDisableMiningUntilSlot` (default 2000, about 5.7 hours) closes the mine
  chain to any transit stamped before that slot, so the nodes and sequencers of
  a fresh network are up before the first transit is contested. Set per genesis
  with `--disable_mining_until_slot` on `proxi init genesis` and `proxi util
  ledger_definitions`; a standalone
  developer ledger opens at 0; the miner waits for the start slot and never
  targets an earlier one. Note the first transit is a long grind: its gap from
  genesis relieves K to the floor, so every miner solves it at once and the
  canonical winner rule (smallest VRF output) decides among solutions stamped
  at the start slot. Built 2026-10-06. Signals not to rush (2026-10-10): the
  miner's banner and wait message give the opening slot as wall-clock time, UTC
  and local, and say an earlier transit is refused everywhere; a node drops a
  mining transaction stamped before the start slot ahead of persist and gossip
  and warns the operator once per slot (`core/resilience.md`, gate table).
- [x] External nonce seekers for `proxi node mine` (2026-10-07): with
  `mine.seeker.listen` in the profile the miner serves its search target as a job
  over HTTP and accepts nonces back, verifying and proving each itself; `--workers 0`
  leaves the search to the seekers. Reference seeker in Rust at
  `proxi/node_cmd/mine/nonce_seeker/`, about twice the Go loop per core, reading the
  same encrypted or plain key file. Spec: `kb/external_nonce_seeker.md`.
- [x] Library commitment proof (2026-10-08): `get_ledger_definition` returns, with the
  library, the latest reliable branch's bytes, the upgrade UTXO and a Merkle proof against
  the baseline root in the branch's stem; `proxi` and the wasm wallet refuse a library that
  does not verify. Closes the one gap the input commitment leaves on a plain-HTTP API path.
  Spec: `kb/library_proof.md`. Nodes older than this refuse to serve a new `proxi`.
- [x] Witness endpoints (2026-10-08): `api.node_urls` in the wallet profile, rendered with the
  three public nodes, and the cross-check of the proof's branch ID against them before any
  library is used; an empty list warns and continues. Spec: `kb/api_witnesses.md`.
- [x] Stability at 30 TPS with 20 sequencers: a load run before take 1.
- [x] Library proof read-through (2026-10-09): the commitment reads the branch bytes through
  the txstore writer cache; the store lags attachment by the writer's flush delay and a
  wallet call right after a new LRB failed.
- [x] Sequencer resume and start guard (2026-10-09, after the mini-testnet fork): the frozen
  coverage delta counts the virtual consumer of the baseline branch's own sequencer output,
  so a branch built on it no longer overshoots the supply and crashes the builder; a
  sequencer waits while peers' branches run more than the bootstrap lag past its own, listens
  a slot before a bootstrap start, and reloads its start tips when the LRB moved. Open: the
  LRB walk-back picks genesis under a persistent fork. `core/resilience.md`.
- [x] Mining stream handshake (2026-10-09): the upgrade request carries `ledger_hash`, the
  library hash at slot 0; a client with another or none is refused with the reason before
  the upgrade (a close frame with the reason, the only form an old miner shows) and its address for 5 minutes, so miners left running from the stopped
  network no longer hold subscriber slots (they cannot submit a valid transaction on the
  new ledger: the floor proof-of-work gate and partial validation drop them before
  persist). `proxi node mine` presents the hash it reads from the slot 0 library.
  `api/api.md`, `core/resilience.md`.
- [x] Spawned nonce seeker (2026-10-09): `proxi node mine --seeker` or `mine.seeker.spawn`
  runs the reference seeker beside the miner, with a free loopback port, a fresh token, the
  key file and its passphrase handed over, output in the miner's log, restart on exit and
  the parent-death signal on Linux; local workers default to zero. `--workers 0` without
  any seeker is refused instead of clamped to one worker. `kb/external_nonce_seeker.md` §4.1.
