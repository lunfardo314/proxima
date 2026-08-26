# Conditional locks: delegate to `sigLock` / `chainLock` where the fallback is equivalent

> **SHIPPED** — audit completed and the one finding fixed. Archived from
> `claude/TODO.md` on 2026-08-26. The code is the truth; this records the rule
> and, more usefully, the cases that were deliberately *not* changed.

## The rule

When a lock's conditional fallback path is meant to behave "like an ordinary
`sigLock` for the issuer" — or `chainLock` for a chain — the body should invoke
`sigLock` / `_sigLock($holder)` (or `chainLock` / `_chainLock($id)`) rather than
hand-rolling a `txHolderID == issuer` or chain-id equality test.

Calling the real thing picks up unlock-by-reference for free, keeps semantics in
lockstep with the base lock, and shrinks the lock body. `examples/dex/dex.easyfl`
is the reference: its sell/buy order reclaim windows just call `sigLock`, and the
bundle shrank by ~110 bytes against the hand-rolled version.

## Audit results

| Lock | Verdict |
|------|---------|
| `lock_tag_along.easyfl` | ✓ target window → `_chainLock($0)`, sender reclaim → `_sigLock($1)` |
| `lock_send_with_deadline.easyfl` | ✓ target window → `_sigLock`/`_chainLock` per `targetType`, master reclaim → `_sigLock($1)` |
| `lock_delegate.easyfl` | ✓ master path → `_sigLock($1)`, target path → `_chainLock($0)`. The frozen / on-hold / safe-revocation logic is delegation-specific, not redundant sigLock logic |
| `lock_chain.easyfl` | ✓ baseline; nothing to delegate |
| `lock_signature.easyfl` | ✓ baseline; nothing to delegate |
| `lock_stem.easyfl` | ✓ uses `signaturePublicKey(txSignatureData)` for the VRF proof — stem-specific, not a sigLock fallback |
| `timelock.easyfl` (htlc) | ❌ → ✓ **the one finding.** The signature path was `equal($0, txHolderID(txSignatureData))`; refactored to `_sigLock($0)`. Both HTLC tests still pass, and reference-unlock now works on the post-deadline path for free |

## What was deliberately left alone

This is the part worth keeping, because the code cannot say why something was
*not* changed.

Produce-side `equal(masterID, txHolderID(txSignatureData))` checks in the
tagAlong, sendWithDeadline and dex order locks are **not** refactor candidates,
for three reasons:

1. They bind the issuer at *create* time, which is a different question from who
   may unlock later.
2. The lock element at output position 2 may not be a `sigLock` at all.
3. `sigLock`'s produce-side rules — `selfEnforceZeroAmountsInNonChainedOutput`
   among them — would be inappropriate to import wholesale into a lock that has
   its own amount semantics.

Anyone re-running this audit will be tempted by those checks. They were
considered and rejected.
