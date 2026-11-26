# Burning the frozen funds

> **SPEC — built on `feat/burn-frozen` (2026-11-26), not released. A ledger change: library upgrade 8, a hardfork.** The 782 outputs left by the 2026-11-17 exploit, **5,005,843.389176 PROX**, leave the ledger state and the ledger's total supply. The crossing branch deletes the outputs as it is committed, and the stem rule lowers that branch's supply by the burned amount. Tentative activation slot **340827** (Friday 2026-11-27 16:00:07 UTC), subject to the rehearsal of section 7. The bonus-lottery draft that was to be upgrade 8 becomes upgrade 9.

## Why burn

- **The tokens should not exist.** They were created out of thin air by the exploit of 2026-11-17: a bug in the evaluator, since fixed (v1.6.10), let the exploiter bypass the ledger's rules for minting tokens. No rule of the ledger allows them; they are illegitimate and are burned.
- **They are idle capital forever.** Frozen, they can never take part in the consensus, yet they count in the supply that health and participation are measured against. 5.0M PROX of dead tokens, about 5% of the supply, makes the consensus look less healthy and the capital less engaged than they are.

## 1. What is burned

Everything the node blocklist (`ledger/blocklist`) still froze, the delegation whose master key it froze, and the one output left on an exploit key outside the list:

| Part | Outputs | PROX |
|---|---|---|
| Frozen sigLock outputs: every output on the freeze list except the one taken off it at slot 265592 | 780 | 4,906,422.634743 |
| The delegation whose master key is frozen (`0003c7d9…7e0e00`, unmoved since slot 247769) | 1 | 99,116.034120 |
| An output on the key of the first mint's signer (`0003a388…9b02`), less than 99% tainted and so never on the list; the only output any frozen key holds outside it | 1 | 304.720313 |
| **Total** | **782** | **5,005,843.389176** |

Of this, at least 5,005,194.4 PROX was minted by the exploit and the rest is change the same keys held (chain balances, funding, rounding). Every output was checked unspent in the latest reliable branch at slot 328972. The full list is in [`burn_frozen_outputs.md`](burn_frozen_outputs.md).

What stays: about **3.59M PROX** of the 8.6M minted (8,600,000 − 5,005,194.4 = 3,594,805.6), essentially what was sold on an exchange before the freeze plus about 7k PROX of fees paid to sequencers, about **4.0%** above the supply there would be without the exploit.

## 2. Why the ledger cannot do it today

- **No transaction can spend the outputs.** 781 are sigLocks whose keys are frozen; nobody but their holders could sign, and nobody should. Removing them takes a change of state no transaction makes.
- **No transaction can lower the supply.** Every transaction balances, produced = consumed + inflation (Go, `transaction.validateOutputs`), inflation never negative; every branch's stem declares `totalSupply == predecessor totalSupply + slotInflation` (`stemLock`, EasyFL).
- **The node checks both at commit.** A branch's computed supply against its stem (`core/attacher/check.go`) and the resulting trie root against the successor's `BaselineRoot` are the two hard invariants: a mismatch rejects the branch, and a node that disagrees with every honest branch stops following. The branch delta is checked for conservation, `created == deleted + slotInflation`, twice: when the branch is attached (`commitBranch`, a graceful shutdown on mismatch) and in the deferred trie update (`updateTrie`).

The burn therefore changes a ledger rule and the commit path, and is a library upgrade: every node on the release before the slot; every wallet and miner stamping transaction version 8 from the slot (`transaction/parse.go` rejects any other version). `proxi` takes the version from the node for each slot (`glb.SetTimestamp`), as at upgrade 7; other signing software must update.

## 3. The upgrade

- **Library upgrade 8** listed in `ledger.Upgrades` at the burn slot S (`ledger/def_upgrade8.go`, `ledger/def/upgrade8.easyfl`), with the burn set committed in the library: `constBurnedAmount` (5,005,843,389,176 motes) and `constBurnedListHash` (blake2b-256 of the 782 output IDs, sorted, concatenated). The list itself is Go data in `ledger/freezeburn`; a test pins both constants to it, so the library hash, and with it the library proof (`kb/library_proof.md`), covers the list.
- **`ledger/freezeburn`** holds the whole node side: the list with amounts, the 784 exploit keys, the freeze until S, the crossing test and the deletion. Until S it refuses any transaction consuming a listed output (at every slot) or signed by an exploit key (from slot 254500), through the same check as the blocklist and as a policy refusal (`blocklist.ErrFrozen`).
- **The blocklist mechanism stays; its tables are empty.** `ledger/blocklist` freezes nothing from this release on; its former tables (the 781 outputs, the 784 keys and the output unfrozen at slot 265592) are kept as commented history in its `list.go`.
- **The crossing branch.** The first branch on each lineage whose stem predecessor is before S and whose own slot is S or later. EasyFL tests `slotOf(predecessor stem) < S` (the library applies only from S); Go tests that the library of the branch's slot carries upgrade 8 and that of its baseline does not (`freezeburn.IsCrossing`), the same on a live ledger and never true on one whose genesis folds the upgrade. Several competing crossing branches each act on their own baseline, the lesson of upgrade 1 (`kb/library_upgrade1.md`): never a process-wide flag, never commit order.
- **After S** the listed IDs no longer exist; `freezeburn` keeps refusing a spend of one at the gate, so it is dropped instead of waiting for inputs. The exploit keys are free from S: they hold nothing.
- **Health needs no special handling.** It is measured against the stem's total supply, which drops by the burned amount; no node-policy subtraction.
- **Genesis folding** (`ledger/upgrade.md`): a new ledger folds every upgrade from slot 0 and holds none of the outputs. Folded, the upgrade's slot is 0, no predecessor is older, and nothing crosses: the burn drops out by itself.
- **Tools that assume supply only grows:** `multistate.FetchSummarySupply` (`proxi db info`) subtracts unsigned and would show a wrapped number across the burn; `proxima_lrb_chain_inflation_total` (supply growth minus bonus minus mined, clamped at 0) undercounts in the one sample spanning the burn. Both get the burn term.
- **Explorers and the dagstore** see outputs leave the state without a consuming transaction, as the upgrade UTXO enters it without one. The chain explorer shows the delegation chain ending.

## 4. The design: deletion at the slot

**Mechanism.**
- **Ledger:** `stemLock` replaced (as in upgrade 3): `totalSupply == pred + slotInflation − _burnedOnCrossing`, the latter `constBurnedAmount` when the predecessor stem is older than S and 0 otherwise.
- **Attacher** (`commitBranch`): on a crossing branch it checks that the baseline holds all 782 outputs, refusing the branch otherwise, and adds their deletion to the branch's mutations: each output with its account index entries (for the delegation both, under the master and under the target) and its transaction's unspent-outputs record, as consuming it would, plus the chain record of the delegation, as when a chain ends; so every reader of the pending branch already sees them gone. The supply it computes (`finals.Supply`, `enforceStemValues`) is the baseline's plus the slot inflation less `freezeburn.BurnedAt`. The conservation check on the branch's own transactions is unchanged; the deferred trie update checks `consumed + inflation == produced + burned`, the burned amount carried in `RootRecordParams.Burned`.
- **Sequencer:** `SeqTxBuilder.SuccessorSupply`, used by the stem builder and by the proposer's health gate, subtracts the burned amount on a crossing branch.

**Consistency with the DAG semantics (`kb/dag_semantics.md`).**
- **Coverage is untouched.** The coverage delta of a branch is the value its past cone consumes beyond its baseline; no transaction consumes a burned output, so the deletion adds nothing to it, and it is appended to the mutations after the delta is computed. The stored ledger coverage, `(L(p) >> K) + Δ`, and D8's absolute coverage `A`, computed from it and from `Δ`, are therefore the same as without the burn, on every node. Only two values of the crossing branch change: its declared supply, and with it its health (computed against the lower supply), and the root of the state it commits.
- **Both follow from the baseline alone.** The deletion set is fixed (the list), applied to the branch's own baseline, which must hold all of it; the supply term is fixed (`BurnedAmount`) and gated by the slots of the branch and its baseline. So every node commits the same root and supply for every crossing branch, in whatever order it attaches competing ones.
- **A node attaching the crossing branch later** takes the same path: forward sync and txstore replay attach it on its own predecessor, which holds the outputs, and delete them as a live node did. A node restoring a snapshot taken after the slot starts from a state that no longer holds them, and every branch it attaches has a baseline at or after the slot, so none crosses and nothing is deleted twice. A branch re-attached against a foreign baseline (the snapshot anchor, `recomputedAgainstForeignBaseline`) crosses only if the anchor is before the slot, and then the anchor's state still holds the outputs.

**What changes for transactions:** nothing but the version. No new lock, no change of the balance rule or the branch's shape; the branch transaction looks as it does today, only its declared supply is lower.

**If an output is missing at S.** It cannot be on an upgraded node: `freezeburn` refuses every spend. A lineage that spent a listed output under an old build cannot cross S, since its crossing branch is invalid by the rule above; that is intended.

**Alternative not taken.** The outputs could instead be consolidated at the crossing branch into one output under a new burn lock, with the same total, and a later branch could consume it with negative inflation, so that the burn is a visible transaction. It was left out for its added complexity and risk: it touches three more rules every transaction or branch passes (the balance rule, the branch's two-input shape, a new lock) and the sequencer's branch builder, spreads the change over two steps, raises an open question on the burning branch's coverage, and leaves a reusable burn mechanism in the ledger.

## 5. Risks and consequences

**Short term (the activation and the slots around it).**
- **One commit carries the risk.** Every node must delete exactly the 782 outputs from each crossing branch's own baseline and subtract exactly the constant. A node that gets either wrong disagrees on the trie root or the supply of every crossing branch, rejects them and stops following: the shape of the upgrade 1 split. The rehearsal checks exactly this, and the change is small enough that there is little else to check.
- **Every sequencer must be on the release before S.** An old node rejects the crossing branch and every transaction of version 8, and stops following; an old sequencer can no longer issue valid transactions.
- **Every signing wallet and miner must stamp version 8 from S.** `proxi` does so by itself; other signing software must update, or its transactions are refused from S.
- **Branchless slots.** If no branch is made at S, the first branch after it crosses; the test is on the predecessor's slot, not on S itself.
- **Several crossing branches** in one slot each delete from their own baseline; whichever wins, its lineage carries the burn exactly once.
- **The attacker's delegation chain ends** without a transition. Its target, an outside sequencer, could still try to consume it; from the release such a milestone is refused, and after S its input does not exist.

**Long term.**
- **A one-off constant in `stemLock`** for as long as the ledger lives: every later replacement of `stemLock` must carry it, and the fold must keep omitting it.
- **The record of the burn** is a drop in the stem's supply at one branch and a state change with no transaction. Explorers, the dagstore and any audit that sums issuance must know the constant and the slot; this spec and the library constant are the record.
- **No reusable mechanism** is left in the rules: a later burn would need a new upgrade of its own. Nothing in the transaction rules changes.
- **The ledger afterwards:** 782 fewer outputs, a supply 5,005,843.389176 PROX lower, and health, participation and every supply-relative figure measured against the real supply with no node-side correction.

## 6. Consequences for the tokenomics

Figures at the latest reliable branch of slot 328972 (2026-11-26 06:17 UTC), the census at slot 328953.

| | Before | After the burn |
|---|---|---|
| Ledger total supply (stem) | 97,950,423.9 | **92,944,580.5** |
| Coverage delta of the branch | 80,914,950.8 | unchanged |
| Consensus health (coverage delta / supply) | 82.61% | **87.06%** |
| Coverage delta a healthy branch needs (7/12) | 57,137,747.3 | **54,217,672.0** (−2.92M) |
| Margin above the 7/12 line | 24.3 points | **28.7 points** |
| How far the coverage delta may fall before a branch is unhealthy | 23.78M | **26.70M** |
| Idle capital (census) | 15,610,447.4 (15.94%) | **10,604,604.0 (11.41%)** |
| Capital participation (supply less idle, of supply) | 84.06% | **88.59%** |
| Bootstrap capital share (at its inflation cap) | 61.92% | **65.26%** |
| Mined share | 28.04% | **29.55%** |
| Annual inflation cap (rate on supply) | 25.77% | **26.62%** |

**Supply.** The stem's total supply falls by 5,005,843.389176 PROX in one branch, the slot's net issuance about −5.0M instead of the usual +60 to +100 PROX a slot. From then on supply grows as before (0.72M mined and 0.024M of inflation a day). Inflation does not change for any holder: chain inflation is anchored on `constTargetBaseSupply`, not on the current supply, and the burned outputs earned none. Mining is untouched: its remaining amount and ceiling (`I + R_init`) are separate from the supply.

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

**How the monitor shows it.** The monitor reads the stem, so supply, health, participation, the bootstrap and mined shares and the inflation cap all move by themselves at the burning branch, to the values above. Added for clarity: fields `burned` and `burned_slot` and a line under the total supply tile, "burned 5,005,843.39 PROX at slot S", from `freezeburn`. The census loses the 782 outputs from idle capital and from the classes table. The decentralization milestones tile ("mined > k × bootstrap capital") does not move. `proxima_lrb_supply` drops by the burned amount at the burning branch; Grafana panels of supply growth show one negative step.

## 7. Release and rehearsal

- **Built 2026-11-26** on `feat/burn-frozen`. Tests: `ledger/freezeburn` (the list against the library constants, the freeze, the deletion, nothing crossing on a folded ledger), `ledger/blocklist` (the mechanism on planted entries, the tables empty), `ledger/tests/burn_state_records_test.go` (a plain output, a chain and a delegation burned through the real trie update leave no output, account index entry or chain record, and nothing else moves), the ledger suite with upgrade 8 folded into its genesis, and `tests/TestBurnAcrossUpgrade8`: sequencers across the slot on an unfolded ledger with a burn set of two outputs, checking that no branch from the slot holds them, every crossing branch's supply drops by the burned amount and every other's grows by its inflation, and no milestone turns BAD; passed 2026-11-26 (four competing crossing branches on one baseline, each burning from its own, 124 branches after the slot, 0 BAD of 10,615 milestones). `core/...`, `sequencer/...` and the touched ledger packages pass under `-race`. Still to do: the multi-node rehearsal, release notes, announcement.
- **Before release, a local multi-node rehearsal**, with S moved to a near slot and a burn set created on the rehearsal ledger: several sequencers producing competing crossing branches; a node restarted across S; a node syncing from a snapshot taken before S, and one forward-syncing across S from behind; a node restoring a snapshot taken after S; a node on the previous release, which must stop following at S and nothing else; a low-health slot after S (a sequencer stopped). Checks: every upgraded node commits the same root and supply for every crossing branch, the snapshot-restored and forward-synced ones included; the latest reliable branch agrees across nodes; `proxi` transactions of version 8 pass from S on.
- **Slot.** 340827 holds only if the rehearsal passes in time for every own node and the outside sequencers to update; otherwise the slot moves. A pending upgrade may be moved by a later release before its slot.
- **After S:** compare supply, root and latest reliable branch across own nodes for the first slots; check the monitor and `proxima_lrb_supply`.

## 8. Not in this release

- **The docs site** (`overview/fair_launch.md`): the supply figures and the projections table, once the burn is in the ledger.

## Annex: the burned outputs

The full list, the output ID, holder, lock and amount of each of the 782 burned outputs, is in [`burn_frozen_outputs.md`](burn_frozen_outputs.md). Total 5,005,843.389176 PROX; all 782 were unspent in the latest reliable branch at slot 328972.
