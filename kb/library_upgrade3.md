# Library upgrade 3: a frozen delegation is always credited, the branch VRF chained on the output

> LIVE, listed at slot 60000 (2026-10-25 09:12:19 UTC), release v1.3.0 (`5ad53618`). Third
> live library upgrade, no reset. Two fixes: report LPX-014, a critical mint and coverage
> forgery through a negative frozen-coverage cell, received privately 2026-10-24; and the
> grinding of the next branch's bonus through a freely chosen VRF proof, reported 2026-10-22
> by the operator of MORA (section "Branch VRF message"). Prerequisite: upgrade 2 active (slot
> 45000). Mechanism: `ledger/upgrade.md`. Supersedes the earlier upgrade 3 draft (a branch
> must extend its issuer's pre-branch milestone), dropped for the node-only coverage
> adjustment.

## Disclosure

LPX-014 was kept out of the repository until the upgrade was active: the release notes,
commit messages and comments of v1.3.0 named the VRF fix and "other fixes: stricter delegation
state transitions" only. The full account (this document and the comments in
`ledger/def/upgrade3.easyfl`, `ledger/transaction/delegation_check.go`,
`sequencer/txbuilder_seq/txbuilder_seq.go`) was published in v1.3.1p1, after slot 60000. The
comments of `upgrade3.easyfl` do not enter the compiled library: the built library's hash is
the one committed on the network at the slot.

## The bug

A sequencer chain's frozen-coverage cells are signed: a stop subtracts. The target is credited
only when it freezes a delegation itself: the delegation output then carries the frozen amount in
its own cells, and the target's chain output the same on top
(`evalEnforceFrozenCoverageOnDelegateOutput`, `evalEnforceFrozenCoverageOnNonDelegationChain`).

Two other paths produced a delegation already **frozen**, with all-zero cells forced and so no
credit:

1. **From a plain chain.** A sigLock chain transits straight into a frozen `delegateLock`
   output. The Go check forces zero cells ("predecessor is not a delegation"), and
   `_validInflationAdvanceProduced` skips its checks for a non-delegation predecessor.
2. **By the master.** The master unlocks an unfrozen delegation and re-locks it frozen. A
   master-unlocked predecessor forces zero cells as well, and with advance share 0 the advance
   check requires nothing. The original report missed this path.

The target then puts the delegation on hold, and the on-hold vector
(`MakeFrozenCoverageAmountDeltasForRevoking`) debits **minus the balance** for every epoch still
frozen, credit or no credit. The conservation equation `2*succ = pred + sum` has no sign check,
so the target chain ends with cells of -B. Every reader takes them as unsigned:
`Output.InflatableAmount`, `ledger.Coverage`, `_predecessorInflatableAmount` and
`_seqChainCoverage` in EasyFL. With B larger than the target's balance A, `A + uint64(-B)`
does not overflow EasyFL's `add`, and the next transition claims `chainInflationOneSlot(2^64 -
(B - A))`, about 608,742 PROX per slot. The same wrapped number enters the branch coverage.
Anyone who runs a sequencer can do it, with two dust-sized chains of their own.

Side effect: when the network's total positive frozen coverage is smaller than the negative cell,
the stem aggregate goes negative and `accumulateFrozen` asserted, so the sequencer exited.

## The fix

**Invariant:** a delegation in the frozen state carries non-zero frozen coverage, that is, its
target was credited for it. A fresh freeze carries its balance; a top-up continuation carries
the increase, and the master's tag-along always adds a positive amount.

`ledger/def/upgrade3.easyfl` replaces two private functions of `lock_delegate.easyfl`. Same
arity, pure EasyFL, no new embedded function:

| Function | Change |
|---|---|
| `_validLimitsProducedFrozen` | a produced frozen delegation requires a chain predecessor that is a `delegateLock` unlocked by its target (`!!!only_the_target_can_freeze_a_delegation`). Closes both paths. |
| `_requireUnlockableByTheTarget` | on a delegation frozen in the transaction (stop or top-up), the target's unlock requires `frozenCoverageBound` of the delegation's amounts to be non-zero (`!!!frozen_delegation_without_frozen_coverage_cannot_be_unlocked_by_the_target`). |

The first rule alone leaves the delegations frozen without credit **before** activation: they
stay frozen for up to 60 epochs, and a stop after the slot would still debit. The second rule
defuses them. Such a delegation stays frozen until it expires, and the master takes it back then.

Not changed:
- the Go embedded checks;
- the on-hold vector computed from the balance, now correct by the invariant;
- the conservation equation.

A sign check on every cell of a chain output would need a new embedded function. The upgrade
builder takes EasyFL source only, and a Go-only rule would not be gated by the slot. With the
invariant held, no negative cell can arise from the slot on.

**Node side, no ledger change:** `accumulateFrozen` (`sequencer/txbuilder_seq/txbuilder_seq.go`)
returns an error instead of asserting. A cone holding a negative cell, possible until
activation, refuses that branch, and the proposer logs `FAIL_AT_MAKETX` and goes on.

**Pre-activation gate, node policy:** `transaction.CheckDelegationTransitions`, called after
constraint validation in `core/vertex` (`ValidateConstraints`), applies the same two rules to
transactions in slots 52000 to 59999, the window between confirming the report and the
upgrade slot. A node of v1.3.0 or later refuses such a transaction and everything built on
it, so with the bootstrap sequencers upgraded an attacker's branch could not become the latest
reliable branch. It does not stop a node still on v1.2.x from including one in its own
branches; those nodes stop following the network at the slot anyway. History before slot
52000 is not re-checked, so re-validation of older transactions is unchanged.

## Checks before and around activation

- **Live state, 2026-10-24** (LRB slot 52251, all three public nodes agreeing): 332 chain
  outputs (23 sequencers, 308 delegations, the mine chain), no negative frozen-coverage cell on
  any non-delegation chain, no frozen delegation with zero frozen coverage; largest cumulative
  chain inflation of a sequencer about 19,270 PROX. Prometheus over 10 days: largest
  2-minute rise of the network's chain inflation about 510 PROX, against at least 608,742
  for a single mint. The hole had not been used.
- **Deployment:** all ten testnet nodes on v1.3.0 the same day, one lineage, no refusal by the
  gate.
- **Rehearsal:** nine local nodes, upgrades at slots 5, 110 and 215; activation everywhere, one
  lineage, no root mismatch, no VRF failure, a node synced from genesis and one restored from a
  pre-slot snapshot following, a foreign library refused at start, the previous release
  stopping at the slot.
- **Tests:** `ledger/tests/library_upgrade3_test.go` (upgrade at test slot 210): both freezes
  refused from the slot, the honest freeze, top-up and stop cycle unchanged. Reproductions of
  the attack itself (valid before the slot, the mint over 100,000 PROX in one slot, an
  uncredited freeze refused a stop and a top-up after the slot, the gate refusing both freezes
  in its window) were run and are not in the repository. The delegation suites of `tests/`
  passed with the gate forced on every transaction.

## Branch VRF message

### The bug

A branch's bonus is drawn from the ECVRF output beta of the proof on its stem, and that part is
sound: beta is unique per (key, message). The message, though, was the predecessor stem's
**proof** concatenated with the slot (`stemLock` in `lock_stem.easyfl`, the builder in
`txbuilder_seq.go`). In ECVRF the proof (Gamma, c, s) is not unique: Gamma fixes beta, but c
and s come from a nonce the verifier cannot check, so any nonce gives a valid proof with the
same beta. The issuer of a branch at slot s could therefore make many proofs for it, each with
the same bonus at s and a different message for s+1, and publish the one giving its own key the
largest bonus at s+1, at the cost of about one `Prove` per try. The gain shows when it also wins
s+1, directly (up to 5 PROX) and in the branch lottery: since upgrade 2 the random bonus is meant
to decide between branches of equal coverage, and a grinder has raised its own.

It came in with the switch from an Ed25519 signature to ECVRF (`a49e896f`, audit FECON-2). The
signature was swapped for the proof in place, so the message kept its old shape; the commit and
its comments took "Prove is deterministic" for "the proof is unique", and the test checked only
that `Prove` returns the same proof twice.

### The fix

New `branchVRFMessage(proof, slot)` = `vrfProofToHash(proof) || slot`, and `stemLock` replaced
(same arity, identical except that the consumed branch verifies the successor's proof over
`branchVRFMessage($1, txSlot)`). Every message now depends on the predecessor's unique output
only, so there is nothing to choose.

- **Genesis.** The genesis stem's proof is empty (pinned by `_enforceGenesisStemValues`); every
  other stem's proof has passed `vrfVerify`. For an empty proof the message is the slot alone,
  exactly the old one, so the chain from genesis stays valid under either rule, including a
  future reset that starts with this library.
- **The upgrade slot.** The first branch at or after it hashes a proof made under the old rule,
  a valid proof like any other; nothing special is needed.
- **Builder.** `Library.BranchVRFMessage` (`ledger/lock_stem.go`) evaluates `branchVRFMessage`
  in the library of the branch's slot, and falls back to the old message when that library does
  not define it. The sequencer is the only branch builder; `proxi` and the wasm wallet build
  none.
- **Not changed:** the bonus (`branchInflationBonus`, beta of the own proof); the mine chain,
  whose message is predecessor output ID, slot and nonce, with no proof in it. The winner's
  choice of proof does move the next predecessor ID, as any free byte of its transaction does,
  but an attempt succeeds with probability 2^-K whatever the ID.
- The comment in the base `stemLock` calling the proof unique is corrected; a comment does not
  enter the compiled library (checked: the compiled `stemLock` is unchanged).

### Tests

- `util/vrf` `TestUniqueOutputNotProof` (replaces `TestUniquenessNoGrinding`): proofs made with
  chosen nonces are all distinct, all verify, all carry the same beta.
- `tests/upgrade3_vrf_message_test.go`: real branches from the sequencer's builder on a fresh
  genesis state, upgrade at test slot 110. Branches over genesis on both sides of the slot; a
  pre-slot branch over a pre-slot one, verifying against the old message only; the first branch
  at the slot over a pre-slot one and the next over it, verifying against the new message only.
  With the builder forced to the old message the branch at the slot is refused by `stemLock`
  (`VRF proof check failed`).
- `tests/upgrade3_sequencers_test.go`: five sequencers across the slot (about two minutes): 65
  branches after the slot, 13 slots with competing ones, all holding the upgrade UTXO.
