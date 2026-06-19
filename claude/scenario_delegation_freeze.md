# Scenario: delegation freeze-epoch distribution

Hands-on validation of the `DelegationPool` freeze-epoch optimization
(`claude/delegation_freeze_distribution.md`). Run on the local testnet — bring it
up first with `local_testnet_runbook.md`. Scenario-specific commands + expected
outputs below.

`P="$CLAUDE_JOB_DIR/tmp/proxi"`, `SEQ=<target sequencer chain ID>`.

## Variant 1 — single bootstrap sequencer (simplest)

Target = node0 bootstrap.

**Order matters (dust is single-use — see runbook §Bootstrap):** the wallet starts
with only the 1-mote dust, and every tag-along command consumes a wallet output.
So FUND FIRST with `withdraw`, only THEN `set-params`/`delegate`:

```bash
cd /mnt/c/Users/evaldas/Desktop/proxima/node0
# 1. fund the wallet (dust pays this fee); gives a spendable ~1e9 sigLock output
$P node sequencer withdraw 1000000000 -f
# (repeat / use a larger amount to cover N delegations + fees)

# 2. the bootstrap holds ~100% supply, so its coverage exceeds the per-sequencer
#    freeze UPPER bound and every freeze would be skipped — disable it (on-chain flag):
$P node sequencer set-params --ignore-freeze-bound -f
# expect: committed seqData {"n":"boot","u":true}
```

Then create N delegations to the target (uniform amount → per-epoch count ==
amount spread). `delegate amount` prompts twice and has no force wiring, so pipe
`y`. All from one wallet (same holder) → space the calls for the txsenders
per-holder rate limit. Ensure the wallet holds enough (withdraw more if needed).

```bash
SEQ=9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82
for i in $(seq 1 250); do
  printf 'y\ny\n' | $P node delegate amount 1000000000 -q $SEQ -e 20 >/dev/null 2>&1
  sleep 1
done
```

- amount must be ≥ the minimum inflatable floor (`delegate amount` asserts it and
  prints the floor if too small; 1e9 passed in practice).
- `-e 20` = max frozen epochs cap = full window (N=20 default).

## Variant 2 — small co-sequencer (node1)

Target = node1 (≤10% supply). No `--ignore-freeze-bound` needed (its coverage is
under the upper bound). Exercises competing proposers across sequencers. Bring up
node1 per the runbook; otherwise identical to Variant 1 with `SEQ=<node1 chain ID>`.

## Verify

```bash
$P node sequencer info $SEQ
```

`sequencer info` prints, after the per-delegation list:

```
---- unfreezes by slot ----
   <slot>: <count> (epoch <e>)
   ...
```

**PASS criteria** (spec §2, §8):

- unfreeze epochs spread across the reachable window — roughly N (≈20) distinct
  epochs for the first ~N delegations, not piled on one epoch;
- with uniform amounts, per-epoch counts balanced (no cliffs);
- the latest/longest-freeze epochs fill first (max-index tie-break).

**FAIL signature (pre-fix bug):** all delegations collapse onto a single (top)
epoch, or the spread advances only ~one epoch per slot.

For an amount-weighted check (mixed amounts), sum the per-delegation balances
(printed in the list) per `UnfreezeSlot` and confirm the per-epoch totals are
balanced, not just the counts.
