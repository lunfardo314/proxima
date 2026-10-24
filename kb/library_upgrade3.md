# Library upgrade 3: the branch VRF message chained on the VRF output

> LIVE, listed (written and built 2026-10-24 on branch `develop-upgrade3`, from `develop`
> v1.2.4). Third live library upgrade, no reset, **activating at slot 60000, 2026-10-25
> 09:12:19 UTC**, release v1.3.0. Main fix: the grinding of the next branch's bonus through a
> freely chosen VRF proof, reported 2026-10-22 by the operator of MORA (section "Branch VRF
> message"); plus other fixes. Prerequisite: upgrade 2 active (slot 45000). Mechanism:
> `ledger/upgrade.md`. Supersedes the earlier upgrade 3 draft (a branch must extend its
> issuer's pre-branch milestone), dropped for the node-only coverage adjustment.

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

## Other fixes

- **Delegation state transitions** (`ledger/def/upgrade3.easyfl`, two private functions of
  `lock_delegate.easyfl` replaced, same arity): a delegation is produced frozen only by its
  target, from a delegation (`!!!only_the_target_can_freeze_a_delegation`), and the target
  puts on hold or tops up a frozen delegation only when it carries frozen coverage
  (`!!!inconsistent_frozen_delegation`). No honest path changes: the wallet, the miner and the
  consolidator never build a frozen delegation, and the sequencer freezes only as the target.
  Until the slot, nodes of this release apply the same two rules as node policy to
  transactions from slot 52000 (`transaction.CheckDelegationTransitions`, called after
  constraint validation in `core/vertex`).
- **Sequencer:** an inconsistent frozen-coverage aggregate in the branch being built refuses
  that branch (`accumulateFrozen` returns an error) instead of stopping the node.
- Tests: `ledger/tests/library_upgrade3_test.go`, upgrade at test slot 210.
