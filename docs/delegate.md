## Delegation

_Delegation_ is a way to take part in Proxima's _cooperative consensus_ and earn
inflation on your tokens **without running a sequencer yourself**.

You hand your tokens to a _sequencer_ of your choice. The sequencer uses them to
generate inflation and shares that inflation with you. You keep ownership of the tokens
the whole time and, in most situations, you can take them back immediately. Even in the
one situation where you cannot (while the tokens are _frozen_, explained below), they
always become freely yours again once the freeze period ends.

Throughout this page, commands are shown as `proxi node delegate ...`. You can shorten
`delegate` to `dlg`.

### How delegated tokens are held

When you delegate, your tokens are placed into a special chained output (a UTXO)
controlled by a **delegation lock**. This output is a _chained account_ that moves between
you and the sequencer according to fixed rules that neither side can break (a **covenant**).

At any moment a delegation is in one of these states:

* **unlockable by the owner** — you can spend or withdraw it freely.
* **frozen** — the sequencer is currently using it to generate inflation. While frozen,
  only the target sequencer can touch the output, and only to unfreeze it.
* **on hold** — the freeze has ended (or the sequencer released it on request). The
  tokens are yours again; you can end the delegation or continue it.

### Looking at delegations

* `proxi node balance` (or `proxi node balance -v`) shows your holdings and, for each,
  the form it takes — an ordinary output, a delegation output, or a plain chain output —
  together with its current state (`frozen`, `unlockable by the owner`, `on hold`).
* `proxi node allchains -q` lists all sequencers in the network — your possible
  delegation targets.
* `proxi node allchains -l` lists all active delegations in the network.
* `proxi node allchains -o` (add `-v` for detail) lists delegation owners and their
  delegations.
* `proxi node delegate status` lists all delegations controlled by your wallet.
  `proxi node delegate status <delegation ID>` (add `-v` for detail) shows one
  delegation.

### Sharing the inflation: the inflation cut

A delegation produces inflation, and that inflation is split between you and the
sequencer. The split is set by the **inflation cut** — the share that goes to **you**,
the owner.

The cut is given in _promille_ (parts per thousand): `1000` means 100%, `900` means 90%,
and so on. You choose it with the `--cut` flag; the default is `900` (90% to you, the
rest to the sequencer).

Each sequencer advertises a **profit margin**, also in promille — the minimum it wants
to keep for itself. A sequencer can only accept a delegation whose cut leaves it at least
its margin. In other words, the largest cut a sequencer will grant is
`1000 − profit margin`. Ask for more than that and the sequencer rejects the delegation.

### The advance: the sequencer pays you up front

To make delegation attractive, the sequencer does not wait until the end to pay your
share. The moment it freezes your tokens, it adds your projected share of the inflation
to the delegation output straight away. This prepayment is called the **delegation
advance** — the sequencer's own investment in your delegation.

The delegation is risk-free for the delegator: even if sequencer is down during the freeze period, 
the delegator already has her cut. Sequencer takes all risk, e.g. for the node or the network being down.
Sequencer profit margin reflects that risk and other costs it runs.

The sequencer then earns that money back (plus its margin) from the real inflation the
frozen tokens generate over the freeze period. For this to work the sequencer must
actually hold enough free balance to pay the advance. A sequencer that cannot afford the
advance for the amount and cut you asked for will not take the delegation.

This advance is also why taking your tokens back _early_ may cost you — see "Taking your
tokens back" below.

**Delegation economics is entirely market-driven: delegators want as big cut as possible,
sequencers want their profit, both compete with their peers in the permissionless free market.**

### Checking a sequencer before you delegate

You do not need to delegate blindly. Two commands inspect a target first, and neither
needs your private key:

* `proxi node delegate target_info <sequencer ID>` shows everything about a target: its
  balances and how much it has available for advances, its parameters (profit margin,
  minimum fee, freeze settings), and the current delegation epoch and its boundaries.
* `proxi node delegate estimate <sequencer ID> <amount>` estimates whether that sequencer
  can afford the advance for a given amount and cut. Add `--cut <promille>` to test a
  different cut, or pass amount `0` to ask for the largest delegation the sequencer can
  currently accept.

### Delegating

The simplest form picks a good target for you automatically:

```
proxi node delegate amount <amount>
```

This selects a sequencer at random, weighted so that less-loaded sequencers are more
likely — the preferred default, as it spreads delegations across the network.

To choose the target and terms yourself:

```
proxi node delegate amount <amount> [-q <target sequencer ID>] [-e <max frozen epochs>] [--cut <promille>]
```

* `-q` / `--delegation_target` — the sequencer to delegate to.
* `-e` / `--epochs` — the largest number of freeze epochs you will allow (see below).
  `0` (the default) means "as many as this sequencer allows".
* `--cut` — your inflation cut in promille (default `900`).

Before sending, `proxi` shows an affordability estimate. If your cut is too high for the
sequencer's margin, or the sequencer cannot afford the advance, it offers a workable
alternative (a lower cut, or a smaller amount) that you can accept or decline.

The command creates the delegation output with your tokens and the delegation lock. A few
slots later the sequencer consumes it, adds the advance, and freezes it.

### The freeze period

While frozen, your tokens work for the sequencer (you already have your cut in your delegation output). The length of the
freeze is measured in **delegation epochs**, and **each sequencer sets its own epoch
length and its own ceiling on how many epochs it may freeze for**. So there is no single
network-wide number — use `proxi node delegate target_info` to see a particular
sequencer's values.

As a rough guide, a common epoch is about 600 slots (roughly 1.7 hours), and a sequencer
typically allows up to around 20 epochs (a day or more) of freezing. The ledger keeps
each sequencer's epoch length within 500–2000 slots and its maximum freeze within 8–32
epochs.

The `-e` flag at delegation time lets you cap the freeze below the sequencer's maximum if
you want shorter commitments.

### Taking your tokens back

**Whenever the delegation is not frozen**, you can take your tokens back at any time — the
state is `unlockable by the owner`.

**While it is frozen**, you can ask the sequencer to release it early:

```
proxi node delegate askstop <delegation ID>
```

This sends a securely authenticated stop request. An honest sequencer unfreezes the
delegation right away, moving it to `on hold`. (The shorter alias is
`proxi node delegate stop`.)

Because the sequencer already paid you the advance up front, releasing early before it has
earned that money back would leave it out of pocket. To make the request fair, `askstop`
includes a calculated **compensation** to the sequencer, paid through the request's
tag-along output. Your wallet therefore needs at least that compensation amount available.
If it does not, you cannot force an early release — you must wait for the freeze to end.
(A sequencer is a centralized, trusted party: in principle it could ignore an `askstop`
request, but doing so only delays its own inflation and damages its reputation, so users
would avoid it afterwards.)

In every case, the delegation **auto-unlocks the moment the freeze period ends**. Right
after that there is a **safe revocation window** of 60 slots (about 10 minutes) during
which only you, the owner, can act on the tokens — this prevents the sequencer from
immediately re-freezing them, guaranteeing you a chance to do as you wish.

### After unfreezing: end or continue

Whenever the delegation is not frozen (`unlockable by the owner` or `on hold`) you can:

* **End it** — `proxi node killchain <delegation ID>` returns all the funds to an ordinary
  address-locked output in your wallet.
* **Continue it** — `proxi node delegate chain <delegation ID> [-e <max frozen epochs>]
  [--cut <promille>] [-q <sequencer ID>]` re-delegates the same chain to the same or a different sequencer.
