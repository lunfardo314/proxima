# Library upgrade 7: the bonus market and sequencer bounds

> **LIVE, activated at slot 283000 (Friday 2026-11-20 19:30:59 UTC), release v1.7.0.** Built on `feat/upgrade7`, pure EasyFL (`ledger/def/upgrade7.easyfl`) plus the node's score, the reference sequencer and the wallet. The branch bonus is chosen by the sequencer and sealed, branches tied on coverage are ordered by σ·u − B, a sequencer needs 20,000 PROX of its own or 100,000 of coverage to branch and to freeze delegations afresh, no sequencer transaction other than a branch after tick 119, and anyone may top up a delegation. Further measures of the upgrade, and of the release that lists it, are documented here after the activation slot. Read before touching the branch bonus, the score's tie-break, the sequencer's bounds or its final milestone, the sequencer output's layout, or delegation top-ups.

## Why

The number of sequencers grew by splitting capital. After upgrade 6's 10,000 PROX floor, 117 of 166 active sequencers held 10,000-12,000 PROX of their own (slot 245822), 102 of them started since upgrade 6. The cause is the branch bonus: among branches tied on coverage a VRF draw decided, so every chain was one more ticket, and the ticket was worth far more than the capital behind it earns.

- **The bonus was out of proportion to the supply.** 5 PROX a slot was sized for the full supply (~1,050M at the end of mining), where it is ~16% of chain inflation. On ~80M it was 188% of all chain inflation.
- **A ticket outweighed chain inflation by two orders of magnitude.** Chain inflation of a balance A is A / (M0 + s) a slot, ~10% a year; a branching chain's expected bonus is b / N. At b = 4.9 and N = 100 a chain of 10,000 earned 150 times more from the bonus than from chain inflation.
- **A floor rations tickets but does not price them.** Capital A fields A / F chains, each still earning chain inflation; only the bonus size sets what a ticket is worth.

The bonus also offsets the cost of running a sequencer machine, whose other income (the delegation cut and fees) is volatile. A fixed bonus cannot follow the token price: it is either too small to cover costs or too large, and then draws a rush of new sequencers. So both levers: the bonus priced by the sequencers competing for it, and the right to compete tied to real weight behind the sequencer.

## 1. The branch bonus chosen by the sequencer

**Ledger.** The inflation B on a branch's sequencer output is chosen by its issuer, from 1 mote to `constBranchInflationBonusBaseTail` (5 PROX) inclusive (`_chosenBranchBonus`); the ledger checks only the interval. Before the upgrade B was a VRF draw the ledger recomputed exactly. The three checks of `chain.easyfl` that used it (`_expectedInflationAmount`, `_validCumulativeChainInflation`, `_validCumulativeBranchBonus`) are replaced to read the output's own inflation, and the chain's cumulative bonus grows by the chosen B. The VRF proof stays on the stem, verified and chained as before: the tie-break reads it.

**Sealed bids.** The bonus is committed before any branch of the slot exists and revealed in the branch, so nobody chooses a bid after seeing the others'.

- **The extension element.** Every sequencer output carries one more inline-data element at index 6 (`seqExtensionConstraintIndex`; index 5 is the sequencer data): a serialized tuple, element 0 the bid commitment. Later upgrades add elements at 1, 2, ... and enforce them without changing the output's layout; a rule reads only the elements it knows. Required on every sequencer output from the slot, valid before it.
- **Commit.** Element 0 is the first 20 bytes of blake2b(B ‖ salt), B and the salt 8 bytes each, or empty (no bid: no branch may extend that output). 20 bytes put a second opening of one commitment at about 2^80 hash evaluations.
- **Reveal.** The branch carries the salt as inline data in `TxConstraints[0]` (`constBranchSaltTxConstraintIndex`). The replaced `sequencer` constraint requires the branch's chain predecessor to carry a 20-byte commitment equal to the first 20 bytes of blake2b(B ‖ salt), B the inflation of the branch's sequencer output (`_sealedBidRevealed`). A branch whose chain predecessor was produced before the slot, or is the genesis output, needs no commitment.
- **Hashed, not plain.** The equilibrium bid σ·u / N reveals u, so a plain commitment would let the last committer see every draw.
- **Without state on the sequencer.** The commitment on a sequencer output commits to the `bonus` of that output's own sequencer data, the salt the first 8 bytes of blake2b("proxima bid salt" ‖ private key ‖ slot of the output). The branch reads the bonus from its chain predecessor's sequencer data and recomputes the salt, so the sequencer keeps no record of its bids (`sequencer/txbuilder_seq/sealed_bid.go`). It writes a commitment on every milestone, since it cannot know which will be the final one.

**Node: the tie-break.** In the adjusted coverage (`v3`'s score) and in the exact tie-break, a baseline branch of upgrade 7 contributes

    T = σ·u − B          u its VRF output read as a 64-bit fraction in [0, 1), σ = 10 PROX, B the bonus it chose

in place of its bonus. Exactly:

1. **The draw.** β = `vrfProofToHash` of the VRF proof on the branch's stem; U = its first 8 bytes big-endian; u = U / 2⁶⁴.
2. **σ·u in motes.** σ = `constBranchTieBreakSigma` = 10,000,000, read from the slot's library. σ·u = ⌊σ · U / 2⁶⁴⌋, the high word of the 128-bit product: integer arithmetic, every node computes the same value.
3. **The term.** T′ = σ·u + 10,000,000 − B, less B once more when the branch's sequencer output is consumed in the milestone's past cone. The offset, twice the largest bonus, keeps it non-negative and is the same for every branch (`ledger.TieBreakScoreTerm`, `ledger.TieBreakKey`).
4. **The score.** Consumed, the output's amount (B included) counts in the adjusted delta, so B is taken back out; unconsumed, nothing of B counts. Either way B is out of the score and T′ is in, so consuming the output never changes the score (`attacher.scoreBonusAdjustment`, from `attacher.BonusTermActivationSlot`, 0: with the upgrade).
5. **Exact ties** (`core/vertex/cmp.go`) are broken by the larger T, then the transaction ID; a branch held without its transaction falls back to the larger bonus.
6. **Classical coverage is unchanged**: B stays in it as tokens, and the declared coverage delta, the stem totals, health and the latest reliable branch are computed as before.

Example: branch X asks B = 0.30 PROX and draws u = 0.62, T = 6.20 − 0.30 = 5.90; branch Y asks 0.05 and draws 0.55, T = 5.45. X wins despite asking six times more; Y would have won with u above 0.665.

**Equilibrium.** Among N tied branches the largest σ·u − B wins and its issuer earns B. Asking ΔB more gains ΔB of prize and loses about ΔB / σ of the chance to win, a proportionate loss, unlike a key scaled by 1/B, where halving B wins half the ties outright and starts a price war. The balance is at

    B* = σ / N

stable against deviations either way. Simulated by best response at σ = 10 PROX: 1.95, 1.01 and 0.39 PROX at N = 5, 10 and 28, against 2.00, 1.00 and 0.36.

- **Knowing the draw in advance.** u can be computed before choosing B (the VRF message is the baseline's VRF output and the slot), so each slot is a first-price auction: the equilibrium bid is B(u) = σ·u / N and the winner's expected bonus σ / (N + 1). Nobody can choose u (upgrade 3 chains the message on the VRF output, upgrade 5 fixes the key between windows).
- **Costs, competition, price.** At B* each sequencer's expected bonus income is σ / N² a slot. Sequencers start or stop until that meets their net cost c a slot (the machine cost in PROX at the token price, minus delegation cut and fees): N = √(σ / c), B* = √(σ · c). At a $5 a day machine:

| Token price | c (PROX a slot) | Sequencers | Bonus a slot | Minted a day |
|---|---|---|---|---|
| $0.01 | 0.059 | 13 | 0.77 PROX | ~6,500 PROX |
| $0.10 | 0.0059 | 41 | 0.24 PROX | ~2,050 PROX |
| $1.00 | 0.0006 | 130 | 0.08 PROX | ~650 PROX |

- **Splitting stops paying.** Every added chain lowers B* for everyone. A holder adding k chains to M others earns k σ / (M + k)² a slot, the most at k = M (σ / 4M, about 0.09 PROX at M = 27), less beyond. A holder seeing all its chains' draws bids with the best and acts as one strong bidder: modelled, it gains 3-6% over uncoordinated play up to 10 chains, 19% at as many as all others together, and raises the mint to at most about twice σ / (N + 1); each further chain still brings little (~19 PROX a day from the tenth, against 255-560 under a fixed bonus).
- **σ has two jobs.** It sets the bonus level and the range of the term, [0, σ], which must stay below the coverage gaps it could overturn. Measured over a day (215 sampled slots, 2026-11-16): 92% of slot contests tie exactly at the top, 0.9% have a runner-up within 15 PROX, the term's whole swing (σ·u up to 10, B up to 5).
- **The mint follows the network**: σ / N a slot at equilibrium, about 3,000 PROX a day at 28 competing sequencers, against ~41,000 before.

**The reference sequencer's bid** is the `bonus` key of its sequencer data, in motes, set by `proxi node seq set-params --bonus`. A pointer, so absent stays distinct from 0. Absent, it asks 5 PROX, the top of the interval: the bonus starts where it was and competition pushes it down, which needs operators to act. It is clamped to [1 mote, 5 PROX]. The value is public; it reveals B, not the draw, which stays sealed until the branch. A fixed B is the blind strategy (equilibrium σ / N); bidding by the draw (σ·u / N) is open to other software.

## 2. Sequencer bounds

- **Hard bound unchanged:** a chain started from slot 196000 on holds at least 10,000 PROX of its own (upgrade 6).
- **Soft bound:** a sequencer may issue a branch, issue a milestone in the pre-branch zone, and freeze a delegation afresh only with **own capital ≥ 20,000 PROX or coverage (own capital + frozen coverage) ≥ 100,000 PROX** (`constMinSequencerBalanceProx` redefined, `constMinSequencerCoverageProx` new; `QualifiesForBranch(own, coverage)` in the constants). Everything else stays open below it: milestones from tick 9 to the zone, tag-alongs, top-ups of delegations already frozen. The bootstrap chain is exempt for branches and the zone.
- **Where each is read.** Branch and zone milestone: own capital and current frozen coverage on the produced sequencer output. Fresh freeze: own capital on the target's chain input, or coverage on its produced chain output, so every freeze of a milestone counts toward each one's admission.

**Why two paths.** The sequencer's chain balance is a hot wallet: its key sits on an always-online server. The own-capital path stops at 20,000, so no operator needs more under that key. An operator keeping less, down to 10,000, qualifies by coverage instead, delegating from a cold wallet to its own sequencer: the server's key can freeze that delegation or hold it, not take it.

**Projection** (slot 245822, 166 active sequencers): 28 qualify (16 by both paths, 7 by own capital only, 5 by coverage only) holding 54.7M of frozen coverage; 138 do not, holding 0.4M, 0.7% of frozen coverage, which moves to qualifying sequencers as freezes expire, through the consolidator's re-delegation.

**Node and wallet.**
- The sequencer's branch and zone gate (`belowMinBalance`) and its freeze gate (`BelowMinSequencerBalance`) use `QualifiesForBranch`.
- **Fresh freezes as a batch.** The ledger judges every fresh freeze of a milestone against the coverage after the whole milestone, so the builder admits the planned freezes together (`SeqTxBuilder.SetCoverageBatch`, set by `v3`'s `insertDelegations`). A freeze is admitted when the coverage so far plus the rest of the batch reaches the bound. A batch left short by a freeze skipped for another reason is refused before the milestone is built (`ErrCoverageBatchShort`), and the rest of the slot admits freezes one by one. Without it a coverage-path sequencer whose freezes expired together at an epoch boundary could not get back over 100,000 when no single delegation lifted it (`tests/upgrade7_freeze_batch_test.go`).
- **Targets.** The rating, the consolidator and the miner's placement filter targets by both paths (`client.SequencerCandidates`). `proxi node delegate amount` accepts a target under the own bound when its coverage after freezing this delegation and the unfrozen ones waiting for it reaches the bound. A delegation that would start such a pool alone must therefore lift the coverage over 100,000 by itself.
- `proxi node seq info` shows the bonus asked and the standing against both bounds.

## 3. No sequencer milestone after tick 119

**Ledger.** A sequencer transaction other than a branch is stamped at tick 127 − `constPreBranchQuietTicks` (8) or earlier, at most tick 119 (`checkPreBranchQuietTicks`), the mirror of upgrade 2's rule at the start of the slot. Branches are exempt; untouched: mine settlement (tick 86), bootstrap transactions.

**Why.** A final milestone stamped at tick 127 endorses everyone's earlier finals, so its branch's past cone is a strict superset of theirs and wins on coverage, whatever the tie-break; sequencers doing this took most slots before the upgrade. An endorsement needs at least one tick of difference, so milestones of the same tick cannot see each other: with tick 119 the last allowed, nobody can collect the others' finals, and the top branches tie exactly whatever software the sequencers run. That is the premise of the bonus market: T orders exact ties, and any strict coverage edge beats it. The premise holds only if every sequencer gets its last milestones in before tick 119: the reference sequencer of v1.7.0 to v1.7.3 often missed its closing and final milestones on the live network (its final's build window ran out at tick 121), so its branches could trail by the late additions others folded in. From v1.7.4 the reference sequencer builds them from the closing's last tick, endorsing only milestones a tick older, and its branches tie with the best in almost every slot (`sequencer/v3`).

**Effects.** Eight quiet ticks (~640 ms) before the boundary for every sequencer to receive the finals and build its branch. Little is lost: a milestone after tick 102 consumes only its chain predecessor, so ticks 120-127 added only one more round of collecting. The reference sequencer stamps its final at tick 119 (the final lead 9 ticks, was 7 with the final at 121), and from the upgrade its milestone builds end at tick 121, a later one not submitted, so the final reaches the others before the boundary. Other sequencer software stamping after tick 119 must change: such milestones are invalid from the slot.

**Why a ledger rule and not policy.** If only some nodes ignored late milestones, a branch extending one would solidify on some nodes and not others; a sequencer refusing to follow such branches would break "only the biggest coverage decides".

## 4. Delegation top-up from any sender

**Ledger.** `_topUpAuthorised` (`lock_delegate.easyfl`) is replaced, same arity, without the requirement that the top-up request's sender is the delegation's master. Kept: the guarantor names this delegation, the request is carried by a `tagAlong` output, the balance does not decrease, and the exact amount and advance of the successor's produced arm. `ensureTopUpDelegation` is unchanged. The askstop allowance stays the master's alone.

**Why.** A mining pool, an exchange or any payer can pay into a delegation directly. It costs the master nothing: the tokens become its own; a frozen delegation stays frozen at its pinned share (`kb/delegation_topup.md`); its ways out are unchanged; the payer's tokens are delivered or reclaimable after `constTagAlongSlots`; the sequencer's minimum top-up (floor 90 PROX) stays policy.

**Node and wallet.** The sequencer drops the sender check from the slot on (`sequencer/txbuilder_seq/req_topup.go`, gated on the transaction's slot). `proxi node delegate topup <amount> --delegation <id>` tops up another's delegation from the upgrade; before it the wallet refuses.

## 5. Activation and release

- **At slot 283000** the library upgrade, and with it the score term (`attacher.BonusTermActivationSlot` 0) and the exact tie-break. No gap: between the ledger rules and a later score term every rational sequencer would ask 5 PROX and ties would fall to the transaction ID.
- **The reference sequencer writes the extension element on every milestone from the release on**; it is valid before the slot, so the first branches after the slot find commitments.
- **Sequencer `v2` is removed**: it ranked by classical coverage, which keeps B, and would have favoured the bigger bonus against `v3`'s score, splitting slots in a mixed network. A config naming `v2` starts `v3`.
- **Older nodes** stop following the network at the slot (rehearsed with v1.6.8, whose latest reliable branch stayed at the slot before).

## Tests and rehearsal

- `ledger/tests/library_upgrade7_test.go`: the library constants, the extension element, the quiet ticks, the soft bound for the zone and for fresh freezes, the top-up from any sender, each before and after the slot; `TestFoldedGenesisUpgrade7`.
- `ledger/branch_tiebreak_test.go`: the draw and the term's arithmetic.
- The bonus at its edges: `sequencer/seqdata/branch_bonus_test.go` (a stored 0 asks 1 mote, anything above 5 PROX asks 5 PROX, no key the default), `sequencer/txbuilder_seq/bonus_bid_test.go` (branches committing to and minting 1 mote, a stored 0 and a stored MaxUint64, each validated by the ledger), and in `tests/upgrade7_branch_rules_test.go` a branch minting 0 or 5 PROX + 1 mote refused.
- `tests/upgrade7_branch_rules_test.go`: real branches altered: another salt and a bonus of 0 refused, the honest branch revealing its predecessor's bid. `tests/upgrade7_freeze_batch_test.go`: the batch. `tests/upgrade7_sequencers_test.go`: four `v3` sequencers across the slot, no milestone BAD, every branch minting the committed bonus, a 15,000-own sequencer branching before the slot and not after.
- The test genesis folds every listed upgrade, upgrade 7 included, so the whole `tests/` package runs under these rules.
- **Rehearsals** (local, `.internal/rehearsal_upgrade7/`, six runs 2026-11-16 to 2026-11-19, the last on develop v1.6.18): a genesis folding upgrades 1 to 6, upgrade 7 at slot 130, six `v3` sequencers with bids of 0.5, 2 and 5 PROX (one lowering its bid to 0.1 PROX after the slot, one returning to the default), one under both bounds, one qualifying by coverage only, a sequencer restarted after the slot, a node synced from genesis after the slot, one restored from a snapshot before it, and the previous release beside them, every node policy check of the release on from early in the run; wallets delegating, topping up, starting sequencer chains before and after the slot with the new and the previous `proxi`, running the new and the previous consolidator, one of them across the slot, and topping up and stopping a frozen delegation after it. The last run passed all 19 checks:
  - every upgraded node on one lineage past the slot, the previous release stopped at it;
  - nothing but branches after tick 119;
  - every branch minted the bonus its sequencer committed to, bid changes included;
  - ties went to the lower bids (0.1 PROX 48 and 0.5 PROX 20 of 90 slots after the slot);
  - the sequencer under both bounds stopped branching and resumed when a delegation lifted its coverage; the coverage-path one branched throughout;
  - another's top-up refused by the wallet before the slot and credited after;
  - the previous `proxi` could not start a sequencer chain after the slot (no extension element), this one could;
  - the previous consolidator moved a delegation away from the coverage-path sequencer, this one kept them frozen;
  - a consolidator running across the slot took the new rules at the slot itself, and its first transaction after it settled;
  - a frozen delegation topped up after the slot stayed frozen with the amount added, and an askstop then stopped it;
  - no node policy check refused anything, and no node crashed.
