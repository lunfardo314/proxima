# Credit tokens — research note

Status: **research, undecided.** No implementation.

## Problem

Frozen/delegated tokens secure the ledger via coverage and are paid for by
inflation. That capital is illiquid: to use it for anything else it must be
unfrozen through `askstop` or the safe revocation window. Question: can the
position be securitized instead — mint a transferable claim against
chained-account balance, without diluting base supply.

## Mechanism

New signed (int64) position in the amounts vector at index 1, the *credit
amount*. Default 0 is semantically inert. Existing positions shift:
inflation → 2, frozen coverage → 3.

`A` = token balance (index 0), `C` = credit (index 1).

- **INV-1** per transaction: sum of consumed index-1 == sum of produced index-1.
- **INV-2** chained UTXO: `-A <= C <= 0`.
- **INV-3** non-chained UTXO: `C >= 0`.

Genesis at 0 + INV-1 ⇒ ledger-wide sum of credit is permanently 0.
Circulating credit therefore always equals outstanding debt, which by INV-2
never exceeds the balances backing it. Net asset value of a chained output
is `A + C`.

Notes on the spec as first stated:

- INV-2 needs both clauses; `A + C >= 0` alone does not imply `C <= 0`.
- "Closing a vault requires input credit to sum to 0" is stronger than
  INV-1..3 require, and the weaker rule is better: debt `-x` need only land
  somewhere legal — extinguished against consumed credit, or moved to
  another chained output with enough balance. Debt is freely transferable
  between vaults. Don't forbid it.
- Only the chain owner may set C (in delegation: the master). `delegateLock`
  must pin `C' == C` on the sequencer-driven transition; same for any lock
  with a third-party transition path.

Implementation notes: INV-3 is a carve-out in
`selfEnforceZeroAmountsInNonChainedOutput` (`ledger/def/amounts.easyfl`).
The signed conservation sum over ≤256 outputs needs Go-level overflow
handling — next to the existing check in `validateOutputs()`; a wrap there
mints credit from nothing.

## Fundamental difference with Cardano

Cardano leaves delegated funds fully liquid — spendable at any moment, no
lock, no unbonding (the delays people associate with staking are Cosmos
21d, Polkadot 28d, Ethereum's exit queue; Cardano has none, and therefore
no "liquid staking" product either — native staking is already liquid).

It works because an address carries a *stake credential*, and at each epoch
boundary the ledger performs a **global fold over the whole UTXO set**,
summing value per credential. That requires a distinguished global instant:
a total order of blocks advanced by an actor — the block producer —
structurally separate from transaction authors, producing a global state
everyone agrees on. Plus no slashing, so stake is a weight and never a bond.

Proxima removes exactly that. One participant type (token holder), one
message (a transaction, which is itself the state contribution). Coverage
is computed per transaction from its own past cone against its baseline.
There is nowhere to stand to perform a global fold.

And a fold would be **circular** anyway: Cardano's snapshot is an *input*
to leader election, taken from a past consensus has already settled. In
Proxima coverage is not an input to the consensus rule — it *is* the rule.
An aggregate deciding which branch wins cannot be computed from the branch.

(Branches do commit a state root and the LRB is a de-facto lag point, so a
fold at depth N isn't literally unavailable — but using it would make
coverage depend on prior consensus rather than constitute it. Architectural
obstacle, not a theorem.)

**Consequence:** the illiquidity is a design choice *given a global-state
consensus*. Proxima has none, so here it is structural. Securitizing the
position is the only available route to liquidity — the real justification
for this mechanism.

## Why the mechanism fits

INV-1 is purely local: one transaction, its own inputs and outputs, no
external reference. The global zero-sum is never computed by anyone — it is
emergent from a local check plus a genesis initial condition, exactly like
supply conservation. `A + C` netting (below) is the same locality class.
**A second conserved quantity with no new global reference.**

## The tokenomic problem

It is a CDP with same-asset debt: collateral `A`, debt `-C`, 100% minimum.
No oracle, no price risk, no liquidation engine — and also no peg.

- Price ≤ 1 is enforced hard: above 1, mint against collateral you keep,
  sell, repeat.
- Price < 1 is enforced by nothing. Minting is costless (no interest, no
  maturity, no liquidation) and the collateral keeps earning inflation and
  counting toward coverage. Perpetual zero-coupon debt is never repaid, so
  the hoped-for buy-back demand doesn't appear. Supply → all chained
  balances; demand → fee volume. Deep, unstable discount.

**Leveraged coverage.** Mint `C = -A`, sell at price `d`, keep the coverage
weight, recycle the proceeds into more chained accounts, mint again:

```
coverage weight per token spent = 1 / (1 - d)
```

`d = 0.9` → 10x. The biggest-coverage rule assumes the coverage holder
bears the loss if the ledger is subverted; a maxed vault has sold its
economic interest and kept the vote. Worse: `d` is low only while credit is
useless, so **adoption and security are inversely coupled**.

**Fix — `A + C` netting.** The stated rule (credit is not inflation capital,
not storage deposit) constrains the *holder*, not the *minter*. Generalize:
*wherever the ledger weights or rewards a balance, use `A + C`, not `A`* —
coverage, inflation accrual, storage deposit, frozen-coverage aggregates.
Leverage then vanishes identically: minting reduces coverage exactly as much
as it frees liquidity. Not a defensive add-on — if the freeze exists because
coverage requires genuinely committed capital, an un-netted vault
reintroduces precisely what freezing prevents.

## Practical options

### A. Credit with 1:1 redemption + netting

Chain lock accepts a transition executable by **anyone**: consume `x`
credit, take `x` base tokens, successor has `A' = A-x`, `C' = C+x`. Purely
local check, straightforward in EasyFL.

- Hard-pegs credit at ~1. Backing becomes a right, not a statement.
- Is a market-priced instant unstake — the answer to the `askstop` question.
- Requires netting (otherwise option A is the cheap-coverage machine).
- Costs: redemption drains vaults during a confidence loss, coupling
  liquidity stress to coverage (the stETH channel); redemption ordering
  across vaults is a griefing surface; redeeming against a delegated chain
  interacts with freeze accounting.

Minting against a delegated position is gated by the safe revocation window
— only the master may set C, and that path is only guaranteed available in
the window. Mid-window minting would also void the sequencer's prepayment.
Asymmetry: *reducing* net stake must be windowed; *restoring* it (`C → 0`)
only raises coverage and need not be.

What credit buys here is removal of the **exit**, not the window. Liquidate
30% inside a window and the delegation continues — no revocation
transition, no interval with capital undelegated, reversible by buying
credit back instead of re-delegating. Versus revoke-and-return: two full
delegation lifecycles plus a dead gap.

### B. Credit without redemption

A credit holder can never touch `A`. "Backed" confers no right — it is a
solvency statement about a counterparty that cannot be called. **Backing
without redemption is decoration.** Compensating property: a credit collapse
never moves a collateral token, so there is no run channel on security at
all. But no peg either, and option C already delivers an unbacked IOU with
no hardfork. Does not justify a breaking change.

### C. Tag-along paid in foundry native tokens

Requires **no ledger change at all**. A sequencer issues a native token and
accepts it for tag-along; what counts as valid payment when picking up
backlog outputs is already sequencer policy. Native tokens are inherently
not inflation capital and not storage deposit, so the "must not be
stakeable" property is free. A sequencer wanting a peg can offer redemption
against base tokens by its own covenant.

- Pros: zero protocol risk, zero consensus coupling, available today,
  permissionless innovation without a hardfork.
- Cons: trust in the issuer, fragmented per sequencer, no ledger-enforced
  backing.

Per-sequencer discount rates are antebellum free banking — wildcat notes
priced per issuer by bank note reporters. It was a UX disaster and is why
uniform currency won. That cost lands on option C, but note it lands on
credit too the moment sequencers set their own credit discounts.

**Both C and credit hit the same floor:** a tag-along output still needs
~13.6M motes of storage deposit in base tokens. Neither eliminates idle base
tokens; both only recover the running fee *float*. If the float is small
relative to the deposit, the tag-along motivation dissolves entirely —
compute this before using it as justification.

### D. Reserve the index, decide later

Insert a must-be-zero position at index 1 in the fairlaunch hardfork, shift
inflation → 2 and frozen coverage → 3. One migration instead of two,
commits to nothing.

## Recommendation

Do not ship credit unscoped. Either commit to **A** (redemption + netting)
and present it as a market-priced instant unstake — a better feature than
the tag-along story ever was — or take **C** and leave it to sequencers.
**D** is cheap insurance either way.

Against shipping the open-ended primitive: there are no policy knobs (no
debt ceiling, no interest, no adjustable ratio — Maker has all three), and
once negative C exists on live outputs index 1 can never be removed without
a confiscatory forced-settlement hardfork. Every parameter freezes at first
use. Plus a permanent obligation on every future lock to pin C, and two
units throughout the wallet (balance, coin selection, fee estimation, chain
views, `txapi`, explorer).

## Prior art

Economics are ~15-year-old CDP design; the encoding (debt as a negative
entry in the same amounts vector, conserving to zero) is unusual for a
production UTXO chain.

- **MakerDAO/Sky** — direct ancestor; differs by oracle, overcollateralization, liquidation, stability fee.
- **Liquity** — 110% min, 0% interest, redemption at face value against the riskiest vaults. The model for option A.
- **Liquid staking (Lido, Rocket Pool, Marinade, Jito)** — closest by purpose. They hold a peg because they are pooled and redeemable via an unstake queue; credit as proposed is per-vault and non-redeemable.
- **Cardano** — the opposite answer to the same problem; does not transfer (above).
- **Terra/Luna** — cautionary case for a circulating claim minted against the native token.

## Separate note: unfreeze window clustering

The epoch grid is **per sequencer, by design**. `lock_delegate.easyfl:60`:

```
func delegationEpochOffset : mod( slice($0, 0, 3), $1)   // $0 = TARGET chain ID
```

Each sequencer gets its own epoch grid on the ledger time axis with a
pseudo-random offset. It must stay that way: the epoch *number* is a shared
coordinate across all delegations to that sequencer, and that is what makes
frozen amounts manipulable as a total per epoch. Deriving the phase from
the delegator would make "epoch N" a different slot range for every
delegation and destroy that aggregation.

Window spreading therefore already comes from two places:

- **Across sequencers** — distinct grid offsets per target chain ID.
- **Within a sequencer** — `_selfLastFrozenEpoch` is per delegation, so at
  any boundary only the delegations whose last frozen epoch is the one just
  ended open a window. The rest stay frozen through it.

Residual open question: expiries can still cluster on the same epoch
*number* — e.g. delegations created in a burst that all take the maximum
frozen epochs. If that needs mitigation it belongs in the choice of last
frozen epoch, or a cap on how much may expire per epoch — not in the grid
phase.

## Open items

- Tag-along float vs. 13.6M storage deposit — the arithmetic that decides
  whether the fee motivation exists at all.
- Audit every lock with a non-owner transition path for C pinning.
- If option A: redemption ordering across vaults; interaction with freeze
  accounting.
- Whether clustering of `_selfLastFrozenEpoch` on a single epoch number
  needs an explicit mitigation.
