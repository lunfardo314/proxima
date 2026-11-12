# sequencer v4

`v3` with a last milestone before the branch. Optional and experimental, selected with
`sequencer.version: v4`; `v3` stays the default. Everything not listed here is as in
[`../v3/README.md`](../v3/README.md) and [`../README.md`](../README.md). Design, measurements and
rollout plan: `kb/sequencer_v4.md`.

What differs from `v3`:

* **A last milestone at the slot's last tick.** The pre-branch zone keeps `v3`'s closing and
  final milestones and adds a third, stamped at tick 127. It consumes only its chain predecessor
  and endorses the peers' final milestones, which nobody else can endorse before the branch.
  Its build starts `last_build_lead` ticks before the boundary with the timestamp fixed at tick
  127, ahead of the clock; the receiving nodes, this one included, admit it and attach it when
  the clock reaches it (the `wait` gauge). Like every zone milestone it is skipped when it has
  nothing new to raise coverage with, and it is also skipped when the previous own milestone has
  not attached before the last tick.
* **The final milestone leaves room for it.** With the last milestone on, the final milestone is
  not issued when its timestamp would leave less than the sequencer pace to tick 127, and it is
  retried only until the last milestone's build starts.
* **The branch waits for the last milestone.** After a last milestone the branch is built once
  that milestone has attached, at most `lateBranchToleranceTicks` past the boundary; at the
  tolerance's last tick it goes out on whatever own milestone has attached. Without the wait it
  would extend the final milestone and fork the own chain.
* **Submit time reserved for the fallback.** In `task.Run`, when a base-extend or bootstrap
  proposal is already complete and eligible, the factory's build ends `fallback_reserve_ms`
  before the deadline, so the ready proposal is still submitted in time if the factory's is not.
  Each cut is counted as `reserve cuts` in `SLOT STATS`.
* **Branch timing in `SLOT STATS`.** `branch start` (against the boundary's wall clock, negative
  before it) and `build`, in milliseconds.

## Config

Read by `v4` only, from its own subsection, so a node switching versions does not misread them:

```yaml
sequencer:
  version: v4
  v4:
    last_milestone: true      # the last milestone at tick 127
    final_lead: 7             # ticks before the boundary the final milestone is stamped
    last_build_lead: 2        # ticks before the boundary the last milestone's build starts
    fallback_reserve_ms: 150  # 0 turns the reserve off
```

The values above are the defaults. `last_build_lead` must be at least 1 and below `final_lead`;
an inconsistent set stops the sequencer at start. The sequencer logs the options it runs with
(`v4 options: ...`).
