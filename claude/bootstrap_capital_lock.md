# Bootstrap capital: binding it to the bootstrap chain

Status: **spec, not implemented.** Written 2026-08-15 for the fair launch plan
(`.internal/launch_v2.md`). A genesis-time change, cheapest to land at the testnet reset.

The bootstrap capital stays on the bootstrap chain. Its inflation is free to leave from the
start, a tenth of the principal frees up once mining is projected to be over, and the rest
never leaves.

**Not a new lock.** The lock slot is untouched: the bootstrap chain is held by an ordinary
`sigLock` today and could be held by anything later. The rule is an extension of the **chain
constraint**, in effect only on the bootstrap chain, and it says nothing about who controls
that chain — only that the balance cannot leave it.

Two consequences follow immediately and both are wanted:

- **control is transferable**, exactly like any other chain. Whoever holds the bootstrap
  chain may hand it on;
- **the chain cannot be destroyed.** Discontinuing it is how the balance would otherwise
  escape, so the constraint forbids it — the bootstrap chain exists for as long as the ledger
  does.

---

## 1. The rule

Let `P` = the genesis principal (`constInitialSupply`, 100 M PROX) and `T` = the projected end
of mining, a fixed slot chosen at genesis. Define a balance floor:

    floor(slot) = P            while slot < T
                  9P/10        from T onward

On any transition of the chain whose ID is `constBootstrapChainID`, the chain constraint
additionally requires:

1. **a successor exists** — the chain may not be discontinued;
2. **the successor's balance ≥ `floor(txSlot)`** — anything above the floor may leave to any
   output the transaction likes.

Nothing about signatures, keys or lock kinds. Authorisation is whatever the lock slot already
says, unchanged.

Each clause of the intent falls out of the floor:

| Intent | How |
|---|---|
| the genesis amount is untouchable until `T` | floor is `P` before `T`, so only inflation exists above it |
| its inflation is at the holder's discretion, without limit | inflation accrues *above* the floor and may be taken at any time |
| a tenth of the principal frees up after `T` | the floor drops to `9P/10` |
| nine tenths stay on the bootstrap chain forever | the floor never drops again |

No schedule, no vesting, no counter to maintain. The floor is a pure function of the slot and
the capital's position relative to it is just its balance.

## 2. What transferable control means

Because the rule binds the balance to the chain rather than to a person, the bootstrap chain
becomes an ordinary transferable asset that happens to carry a permanent floor. Whoever holds
it holds the right to its inflation, and to the tenth of the principal that frees at `T`.

That is worth stating rather than discovering later: **the founder can exit by handing on the
bootstrap chain, without the 90 % ever reaching the market.** Compared with an unconstrained
holding this is the better shape for everyone — the capital keeps contributing coverage
forever regardless of who holds it, so the anti-Sybil substance the consensus needs at genesis
never evaporates, whoever ends up owning the stream.

It also means the guarantee does not decay if a key is lost, sold, or compromised. A rule
about the holder would; a rule about the chain does not.

## 3. Why a constraint and not a promise

The launch document's hard requirement is that no personal liability of the founder is
acceptable — contractual, as a promise, or under MiCA. Its weakest point is the obvious
question: the founder holds 100 M and asks you to believe it will not be dumped.

Answering with an undertaking is wrong twice over. A promise not to sell creates reliance,
which is the liability being avoided; and it implies the founder is managing scarcity for
holders' benefit, which is precisely the investment-expectation argument. So the launch
document promises nothing, deliberately.

The constraint answers the same question **without promising anything**. It is code: no
undertaking, no counterparty, nothing owed. Anyone can read the genesis state and check it.
And because it binds the chain rather than the founder, it is not even a statement about the
founder — it converts *"the founder has no obligation"* into *"nobody has the ability"*.

## 4. What it costs

Nine tenths of the bootstrap capital stops being a treasury. It can never fund development,
pay a contributor or seed a faucet — not after `T`, not ever.

What stays spendable is not nothing: **all inflation, at any time, without limit**. At roughly
10 %/yr on a 100 M principal that is a real recurring budget arriving without touching the
principal, so whoever runs the bootstrap chain is funded by the network's growth rather than
by selling its float.

## 5. Where it binds, and where it does not

**A constraint is only as binding as the inability to remove it.** Removing it means replacing
the ledger definitions, and a library upgrade (`ledger/upgrade.md`) activates at an upgrade
slot only if the network adopts it — which needs majority consensus.

So its force follows the same curve as everything else in the launch plan:

- **before the 5/12 crossing** the founder controls a majority of the coverage and could
  therefore carry an upgrade removing it. It is not binding in the strict sense during the
  bootstrap period;
- **after 5/12** no single holder can carry an upgrade alone, and it becomes binding in the
  strict sense, permanently.

Worth stating plainly rather than overselling. What makes it credible before the crossing is
what makes withholding mining incredible: it would be a public, permanent, on-chain act,
visible to everyone, refuting the project's one claim in order to reach money that has no
market yet. The constraint does not have to be unbreakable to be worth having — it has to make
breaking it obvious, and it does.

## 6. Mechanics

The rule lives in `chain.easyfl`, guarded on the chain ID so it costs one comparison on every
other chain transition and nothing else:

    if chainID == constBootstrapChainID:
        require(successor exists)                      // no discontinuation
        require(successorBalance >= floor(txSlot))

The no-discontinuation half has a direct precedent in `lock_delegate.easyfl`, which already
refuses to let a target end a delegation chain
(`target_cannot_discontinue_the_delegation_chain`) by requiring the chain constraint's unlock
params to be non-empty. Same check, different reason.

Constants — three already exist, two are new:

| Constant | |
|---|---|
| `constBootstrapChainID` | exists; selects the one chain the rule applies to |
| `constInitialSupply` | exists; the principal `P` |
| `constBootstrapUnlockSlot` | **new**; `T` |
| `constBootstrapRetainedPromille` | **new**; 900 |

`T` from the emission arithmetic: 900 000 transits at the target pace of 4 slots is
**3 600 000 slots** (~427 days). It is a projection — mining may still be running at `T` or
may have finished earlier. Nothing else keys off it.

Changes: `ledger/def/chain.easyfl` gains the guarded branch; the two constants; and tests for
the floor either side of `T`, refusal to discontinue, that inflation above the floor is
withdrawable while the principal is not, and that ordinary chains are unaffected.
`ledger/genesis.go` needs no change at all — the genesis output keeps its `sigLock`.

## 7. Open

- **Partial vs whole.** This spec frees a tenth of the principal after `T`, alongside all
  inflation throughout. A 100 % floor is a stronger claim and a one-constant change
  (`constBootstrapRetainedPromille = 1000`).
- **A bug here is unrecoverable and lands at the worst moment.** If the branch refuses a
  transition the bootstrap sequencer needs, the network cannot start at all. It wants test
  coverage well beyond its size — the branch path and the delegation-freeze path, not just
  plain transitions — and a test that every non-bootstrap chain is untouched.
- **`T` as a slot cannot adapt.** If mining runs long, a tenth frees up while the launch is
  still in progress. Keying the release to a coverage threshold would track the intent better
  but needs the constraint to read supply or coverage, which is a much larger change. A slot
  is the cheap approximation and its failure mode is mild.
