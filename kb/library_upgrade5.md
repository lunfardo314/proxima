# Library upgrade 5: mine successor pinned in full, sequencer key fixed between windows

> **LIVE.** Activated at slot 153000 (Thursday 2026-11-05 09:44:19 UTC, `ledger.Upgrade5Slot`),
> release v1.5.0. Written 2026-11-03 on branch `feat/upgrade5` (from `develop`) and kept out of
> the repository until activation, because the mine rule shows how to halt the mine chain. Fifth
> live library upgrade, no reset. Prerequisite: upgrade 4 active (since slot 137000). Mechanism:
> `ledger/upgrade.md`. The general rule behind section 1 is in `ARCHITECTURE.md` (section 6 and
> "Traps").

Pure EasyFL, no new embedded function (`ledger/def/upgrade5.easyfl`, `ledger/def_upgrade5.go`).
A node policy ships with it in the same release (see Release).

## 1. The mine successor is pinned in full

**Problem.** `mineLock` did not bound the successor's elements.
A miner who wins one slot can push any constraint onto the next mine output, and it runs on
every later transit: an unreachable timelock halts the mine chain, a signer check gives one key
the rest of the emission. The transit is valid; nothing on the node refuses it. An upgrade
cannot remove the constraint from a committed output.

**Rule.** `_mineSuccessorState` is replaced, same arity: as in genesis, and first
- the successor has exactly 4 elements (`tupleLenAtPath(_mineSuccPath)`): amounts, index
  values, `mineLock`, chain;
- its index values are the predecessor's, byte for byte.

The amounts need no rule: balance and inflation are checked, and `chain` already refuses
frozen coverage on a non-delegation chain (`evalEnforceFrozenCoverageOnNonDelegationChain`), so
an extra cell can only be a zero-bound padding, which binds nobody. Lock arguments and the
chain constraint were already pinned. The number of outputs (2 or 3) and the reward layouts
were already pinned; the reward outputs belong to the miner.

A predecessor carrying an extra constraint from before the slot is still bound by it: the rule
stops new poisoning, it does not heal an output. Verified live 2026-11-03: the mine output has
4 elements on the three public nodes.

## 2. A sequencer chain changes its key only in the first slot of a window

**Problem** (`kb/ledger_upgrade_backlog.md`, "Controller rotation grinds the branch bonus"). A
branch's VRF proof, and so its bonus, is checked under the key that signs it, and the VRF
message of the next branch is known a slot ahead. An operator with N keys moves its chain to the
key with the best bonus for the next branch, winning about N/(N+M-1) of the slots instead of
1/M. Measured 2026-11-03: not exploited.

Three ways to choose the signer, each closed:
- **Change the lock in a milestone.** The successor's holder must be the signer outside a window
  slot.
- **A lock several keys can unlock, or a `chainLock`** whose controlling chain moves to any key
  at will. The lock is unchanged, but the signer is free. The successor must be a `sigLock`, in
  every slot.
- **Move the chain as a plain chain output** in a wallet transaction, then extend it by a
  milestone under the new key. Not detected by a lock comparison, since the milestone keeps the
  plain output's lock. The predecessor of a sequencer transition must carry the `sequencer`
  constraint. This also makes true what the `sequencer` comment always claimed: a regular chain
  cannot become a sequencer chain. Destroying a sequencer chain stays possible; a chain that
  leaves the sequencer form cannot return to it.

**Rule.** `sequencer` is replaced, same arity: as in upgrade 4, plus `_sequencerKeyFixed` on
chain transitions (not origins):
- the chain predecessor carries `sequencer` at `sequencerConstraintIndex`;
- the output's lock is `sigLock`;
- its holder (index value 0) is the transaction's signer, unless
  `mod(txSlot, constControllerChangeSlots) == 0`.

`constControllerChangeSlots = 60`, a divisor of `constDelegationEpochSlots` (600). The windows
are global (slot multiples of 60), not per-chain offsets as the backlog first suggested: simpler,
and the offset buys nothing.

The next transaction must unlock the output, so its signer is the key the output is locked to.
The operator keeps one free choice per window: about one branch in 60, against N/(N+M-1) of all
slots without the rule. A legitimate key change waits at most 60 slots (about 10 minutes).

**Who is affected.** Nobody running our code: the milestone builder
(`sequencer/txbuilder_seq`) re-emits the predecessor's lock and unlocks input 0 by signature, so
every milestone is a `sigLock` to its signer. Chain origins (`proxi node seq init`) are not
checked. `control_succession.md` (not built) must put its handoff milestone in a window slot.

## Release

- `ledger.Upgrades` lists upgrade 5 alone, at `Upgrade5Slot`; upgrade 4 (active since 137000)
  removed, its builder and `Upgrade4Slot` kept (`sequencer/v3` reads `Upgrade4Slot`).
- **Disclosure window and the node guard.** The release published `upgrade5.easyfl`, which shows
  the mine gap, a day or two before the slot; anyone reading it can poison the mine output before
  activation. The same release therefore carries a node policy, active at once, not gated by the
  slot: `Transaction.MineSuccessorIsPlain` (successor with exactly 4 elements), checked
  - at ingress (`validateMineTransit`): such a transit is dropped (`mine_invalid_drop`), neither
    relayed nor streamed to miners;
  - in the sequencer's registry (`checkLocked`): not a valid rival, so our sequencers settle a
    clean competitor instead;
  - in `mine.TransitOf`, shared by `v1`, `v2`, `v3`: kept pending, so never included.
  Policy, not validity: a milestone that includes such a transit is still accepted. Index values
  are not part of the policy (they bind nobody, and requiring them empty could stall our
  sequencers if the live output ever carried one). Residual: a miner addressing the poisoned
  transit to an outside sequencer on an older release, which includes it. Keep the lead time
  short and the release notes neutral.
- **Neutral comments until activation.** The mine rule's EasyFL comment, the policy comments
  (`ledger/transaction/tx.go`, `sequencer/mine/transit.go`) and the test showing that a
  constraint pushed before the slot halts the chain (`TestUpgrade5MineSuccessorBeforeTheSlot`)
  were cut from the release and restored after activation. Comments do not enter the library:
  built from the restored source on the network's upgrade 4 library, the upgrade 5 library hashes
  to the one the network serves from slot 153000.

## Tests

- `ledger/tests/library_upgrade5_test.go`: upgrades 1 to 5 at test slots (upgrade 5 at 410, the
  window slot 420).
  - Mine: from the slot an extra constraint or an index value is refused (before it, accepted);
    `TestUpgrade5MineSuccessorBeforeTheSlot`: a constraint pushed before the slot halts the chain,
    also after it. A clean successor is valid, also over a harmless extra from before the slot.
  - Sequencer: a new key is valid before the slot, refused from it, valid in the window slot,
    and refused the slot after. The same key is valid. A `chainLock` output is refused in a
    window slot. A milestone over a plain chain output, moved to a new key by a wallet
    transaction, is valid before the slot and refused from it, window slot included.
- `ledger/tests/library_upgrade5_test.go` `TestMineSuccessorIsPlain`: the node policy refuses a
  successor with an extra constraint, also before the slot.
- `ledger/tests/mine_test.go`: `buildMineTransition` takes `succExtra` and `succIndexValues`.
- `tests/upgrade5_sequencers_test.go`: five sequencers across the slot, upgrades 2 to 5 listed;
  every branch from the slot holds the upgrade UTXO, and branches keep coming.

## Activation

At slot 153000 every own node (sequencers on v1.5.0p1, access nodes on v1.5.0) moved over
without an error. The three public nodes agreed on every branch and state root across the slot,
and branch quality did not change (median 58 sequencers and 96.2% coverage per branch in the 60
slots before, 56 and 96.2% in the 31 after). 58 sequencers were active before and after. One
outside sequencer, NICO, stopped at slot 152999, the last slot before the upgrade: a node on a
release without it. About 26 outside nodes, none of them sequencers, went on sending transactions
of the old version, which the upgraded nodes refuse at parsing.
