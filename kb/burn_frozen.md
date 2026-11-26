# Burning the frozen funds

> **SPEC — not built (2026-11-26), branch `feat/burn-frozen`. A ledger change: library upgrade 8, a hardfork.** The 781 outputs still frozen after the 2026-11-17 exploit, **5,005,538.668863 PROX**, leave the ledger state and the ledger's total supply. The crossing branch deletes the outputs as it is committed, and the stem rule lowers that branch's supply by the burned amount. Tentative activation slot **340827** (Friday 2026-11-27 16:00:07 UTC), subject to the rehearsal of section 7. The bonus-lottery draft that was to be upgrade 8 becomes upgrade 9.

## Why burn

- **The tokens should not exist.** They were created out of thin air by the exploit of 2026-11-17: a bug in the evaluator, since fixed (v1.6.10), let the exploiter bypass the ledger's rules for minting tokens. No rule of the ledger allows them; they are illegitimate and are burned.
- **They are idle capital forever.** Frozen, they can never take part in the consensus, yet they count in the supply that health and participation are measured against. 5.0M PROX of dead tokens, about 5% of the supply, makes the consensus look less healthy and the capital less engaged than they are.

## 1. What is burned

Everything the node blocklist (`ledger/blocklist`) still freezes, plus one delegation:

| Part | Outputs | PROX |
|---|---|---|
| Frozen sigLock outputs: every output on the freeze list except the one taken off it at slot 265592 | 780 | 4,906,422.634743 |
| The delegation whose master key is frozen (`0003c7d9…7e0e00`, unmoved since slot 247769) | 1 | 99,116.034120 |
| **Total** | **781** | **5,005,538.668863** |

Of this, 5,005,194.4 PROX was minted by the exploit and the rest is change the same keys held (chain balances, funding, rounding). Every output was checked unspent in the latest reliable branch at slot 328972. The full list is in [`burn_frozen_outputs.md`](burn_frozen_outputs.md).

What stays: about **3.59M PROX** of the 8.6M minted (8,600,000 − 5,005,194.4 = 3,594,805.6), essentially what was sold on an exchange before the freeze plus about 7k PROX of fees paid to sequencers, about **4.0%** above the supply there would be without the exploit.

## 2. Why the ledger cannot do it today

- **No transaction can spend the outputs.** 780 are sigLocks whose keys are frozen by the blocklist; nobody but their holders could sign, and nobody should. Removing them takes a change of state no transaction makes.
- **No transaction can lower the supply.** Every transaction balances, produced = consumed + inflation (Go, `transaction.validateOutputs`), inflation never negative; every branch's stem declares `totalSupply == predecessor totalSupply + slotInflation` (`stemLock`, EasyFL).
- **The node checks both at commit.** A branch's computed supply against its stem (`core/attacher/check.go`) and the resulting trie root against the successor's `BaselineRoot` are the two hard invariants: a mismatch rejects the branch, and a node that disagrees with every honest branch stops following. The branch delta is checked for conservation, `created == deleted + slotInflation`, twice: when the branch is attached (`commitBranch`, a graceful shutdown on mismatch) and in the deferred trie update (`updateTrie`).

The burn therefore changes a ledger rule and the commit path, and is a library upgrade: every node on the release before the slot; every wallet and miner stamping transaction version 8 from the slot (`transaction/parse.go` rejects any other version). `proxi` takes the version from the node for each slot (`glb.SetTimestamp`), as at upgrade 7; other signing software must update.

## 3. The upgrade

- **Library upgrade 8** listed in `ledger.Upgrades` at the burn slot S, with the burn set committed in the library: the hash of the sorted list of the 781 output IDs as a constant, the list itself in Go, checked against the hash at start, so the library proof (`kb/library_proof.md`) covers it.
- **The crossing branch.** The first branch on each lineage whose stem predecessor is before S and whose own slot is S or later; the test `slotOf(predecessor stem) < S <= txSlot` is computable both in EasyFL (`_kSlotsBack` already reads the predecessor stem's slot) and in Go. Several competing crossing branches each act on their own baseline, the lesson of upgrade 1 (`kb/library_upgrade1.md`): never a process-wide flag, never commit order.
- **The release before the slot** adds the delegation to the frozen outputs, so its target cannot move it before S and the set at S is exactly the annex. The blocklist stays as it is after S: the IDs no longer exist, and an attempt to spend one is refused at the gate instead of waiting for inputs.
- **Health needs no special handling.** It is measured against the stem's total supply, which drops by the burned amount; no node-policy subtraction.
- **Genesis folding** (`ledger/upgrade.md`): a new ledger folds every upgrade from slot 0 and holds none of the outputs, so the fold of upgrade 8 leaves the burn out.
- **Tools that assume supply only grows:** `multistate.FetchSummarySupply` (`proxi db info`) subtracts unsigned and would show a wrapped number across the burn; `proxima_lrb_chain_inflation_total` (supply growth minus bonus minus mined, clamped at 0) undercounts in the one sample spanning the burn. Both get a burn term.
- **Explorers and the dagstore** see outputs leave the state without a consuming transaction, as the upgrade UTXO enters it without one. The chain explorer shows the delegation chain ending.

## 4. The design: deletion at the slot

**Mechanism.**
- **Ledger:** `stemLock` replaced (as in upgrade 3): `totalSupply == pred + slotInflation − (crossing ? constBurnedAmount : 0)`, `constBurnedAmount = 5_005_538_668_863`.
- **Commit:** the crossing branch's commit deletes the 781 outputs from its baseline, beside the upgrade UTXO injection in `branches._commitPendingBranchUnlocked`.
- **Attacher:** a crossing branch whose baseline lacks any of the 781 is invalid, so the deleted amount always equals the constant; computed supply = baseline + slotInflation − constant; both conservation checks become `created == deleted + slotInflation − burned`, with `burned` the constant on the crossing branch and 0 elsewhere.
- **Sequencer:** the stem builder (`txbuilder_seq.buildStemLock`) and the proposer's supply subtract the constant on a crossing branch.

**What changes for transactions:** nothing but the version. No new lock, no change of the balance rule or the branch's shape; the branch transaction looks as it does today, only its declared supply is lower.

**If an output is missing at S.** It cannot be on an upgraded node: the blocklist refuses every spend. A lineage that spent a listed output under an old build cannot cross S, since its crossing branch is invalid by the rule above; that is intended.

**Alternative not taken.** The outputs could instead be consolidated at the crossing branch into one output under a new burn lock, with the same total, and a later branch could consume it with negative inflation, so that the burn is a visible transaction. It was left out for its added complexity and risk: it touches three more rules every transaction or branch passes (the balance rule, the branch's two-input shape, a new lock) and the sequencer's branch builder, spreads the change over two steps, raises an open question on the burning branch's coverage, and leaves a reusable burn mechanism in the ledger.

## 5. Risks and consequences

**Short term (the activation and the slots around it).**
- **One commit carries the risk.** Every node must delete exactly the 781 outputs from each crossing branch's own baseline and subtract exactly the constant. A node that gets either wrong disagrees on the trie root or the supply of every crossing branch, rejects them and stops following: the shape of the upgrade 1 split. The rehearsal checks exactly this, and the change is small enough that there is little else to check.
- **Every sequencer must be on the release before S.** An old node rejects the crossing branch and every transaction of version 8, and stops following; an old sequencer can no longer issue valid transactions.
- **Every signing wallet and miner must stamp version 8 from S.** `proxi` does so by itself; other signing software must update, or its transactions are refused from S.
- **Branchless slots.** If no branch is made at S, the first branch after it crosses; the test is on the predecessor's slot, not on S itself.
- **Several crossing branches** in one slot each delete from their own baseline; whichever wins, its lineage carries the burn exactly once.
- **The attacker's delegation chain ends** without a transition. Its target, an outside sequencer, could still try to consume it; from the release such a milestone is refused, and after S its input does not exist.

**Long term.**
- **A one-off constant in `stemLock`** for as long as the ledger lives: every later replacement of `stemLock` must carry it, and the fold must keep omitting it.
- **The record of the burn** is a drop in the stem's supply at one branch and a state change with no transaction. Explorers, the dagstore and any audit that sums issuance must know the constant and the slot; this spec and the library constant are the record.
- **No reusable mechanism** is left in the rules: a later burn would need a new upgrade of its own. Nothing in the transaction rules changes.
- **The ledger afterwards:** 781 fewer outputs, a supply 5,005,538.668863 PROX lower, and health, participation and every supply-relative figure measured against the real supply with no node-side correction.

## 6. Consequences for the tokenomics

Figures at the latest reliable branch of slot 328972 (2026-11-26 06:17 UTC), the census at slot 328953.

| | Before | After the burn |
|---|---|---|
| Ledger total supply (stem) | 97,950,423.9 | **92,944,885.3** |
| Coverage delta of the branch | 80,914,950.8 | unchanged |
| Consensus health (coverage delta / supply) | 82.61% | **87.06%** |
| Coverage delta a healthy branch needs (7/12) | 57,137,747.3 | **54,217,849.7** (−2.92M) |
| Margin above the 7/12 line | 24.3 points | **28.7 points** |
| How far the coverage delta may fall before a branch is unhealthy | 23.78M | **26.70M** |
| Idle capital (census) | 15,610,447.4 (15.94%) | **10,604,908.7 (11.41%)** |
| Capital participation (supply less idle, of supply) | 84.06% | **88.59%** |
| Bootstrap capital share (at its inflation cap) | 61.92% | **65.26%** |
| Mined share | 28.04% | **29.55%** |
| Annual inflation cap (rate on supply) | 25.77% | **26.62%** |

**Supply.** The stem's total supply falls by 5,005,538.668863 PROX in one branch, the slot's net issuance about −5.0M instead of the usual +60 to +100 PROX a slot. From then on supply grows as before (0.72M mined and 0.024M of inflation a day). Inflation does not change for any holder: chain inflation is anchored on `constTargetBaseSupply`, not on the current supply, and the burned outputs earned none. Mining is untouched: its remaining amount and ceiling (`I + R_init`) are separate from the supply.

**Capital participation.** All 5.0M was idle: the census counts the 780 sigLocks as idle signature-locked capital and the delegation, unfrozen for many epochs, as an idle delegation. They leave both the idle total and the supply; participation rises 4.5 points to 88.6%. The participating capital itself, 82.34M, does not change.

**Health.** No branch carries more coverage than before; the line moves down to where it would stand if the 5.0M had never been minted. A slot needs 2.92M PROX less coverage delta to be healthy, the margin the dead tokens had taken from every slot since the freeze. Near-line slots (split slots, sequencers dropping out) are healthy more often.

**The founder's share and the decentralization milestones.** The bootstrap capital becomes a larger share of a smaller supply, so the founder-side milestones come later:

| Bootstrap capital below | Without the burn | With the burn | Shift |
|---|---|---|---|
| 7/12 (the founder can no longer advance the ledger alone) | 2026-12-04 | 2026-12-11 | +7 days |
| 1/2 | 2026-12-28 | 2027-01-03 | +6 days |
| 5/12 (the ledger advances without the founder) | 2027-01-24 | 2027-01-29 | +5 days |
| 1/6 (below the fork threshold) | 2027-06-15 | 2027-06-17 | +2 days |

Projected from today's supply growth (the reward flat until slot 506250, then rising) and the bootstrap capital at its inflation cap (1.98 PROX a slot). Without the burn the milestones came earlier only because 5.0M tokens nobody can move counted against the founder; they counted against every other holder too.

**How the monitor shows it.** The monitor reads the stem, so supply, health, participation, the bootstrap and mined shares and the inflation cap all move by themselves at the burning branch, to the values above. Added for clarity: a line under the total supply tile, "burned 5,005,538.67 PROX at slot S", read from the library constant. The census loses the 781 outputs from idle capital and from the classes table. The decentralization milestones tile ("mined > k × bootstrap capital") does not move. `proxima_lrb_supply` drops by the burned amount at the burning branch; Grafana panels of supply growth show one negative step.

## 7. Release and rehearsal

- **Today, 2026-11-26:** build on `feat/burn-frozen`; unit tests (the list and its hash, the crossing test, the stem rule either side of S, the conservation checks, the fold); the core packages under `-race`; release notes; announcement.
- **Before release, a local multi-node rehearsal**, with S moved to a near slot and a burn set created on the rehearsal ledger: several sequencers producing competing crossing branches; a node restarted across S; a node syncing from a snapshot taken before S; a node on the previous release, which must stop following at S and nothing else; a low-health slot after S (a sequencer stopped). Checks: every upgraded node commits the same root and supply for every crossing branch; the latest reliable branch agrees across nodes; `proxi` transactions of version 8 pass from S on.
- **Slot.** 340827 holds only if the rehearsal passes in time for every own node and the outside sequencers to update; otherwise the slot moves. A pending upgrade may be moved by a later release before its slot.
- **After S:** compare supply, root and latest reliable branch across own nodes for the first slots; check the monitor and `proxima_lrb_supply`.

## 8. Not in this release

- **The docs site** (`overview/fair_launch.md`): the supply figures and the projections table, once the burn is in the ledger.

## Annex: the burned outputs

The full list, the output ID, holder, lock and amount of each of the 781 burned outputs, is in [`burn_frozen_outputs.md`](burn_frozen_outputs.md). Total 5,005,538.668863 PROX; all 781 were unspent in the latest reliable branch at slot 328972.
