## Participating in the open testnet

The Proxima testnet is an experimental network for testing the node software and the
various aspects of the Proxima concept.

It normally runs at least 9 nodes, several of which are sequencers. Because the network
is experimental and changes often, we usually keep control of it by holding the majority
of the token supply. Whenever a change is incompatible with the previous version, the
ledger is reset from genesis and everyone starts again from a fresh state.

### Other docs

Please read at least the basics on [proxi](proxi.md) and [delegation](delegate.md), along
with any other available materials.

### Public access points

These are public API endpoints you can use from `proxi` (set one as `api.endpoint` in your
`proxi.yaml`) or for other purposes:

* http://113.30.191.219:8001
* http://63.250.56.190:8001
* http://83.229.84.197:8001
* http://5.180.181.103:8001

### Web tools

Each public access node also serves a few read-only browser tools on its API port. Open
them at `http://<access point>:8001/<path>` — for example
`http://113.30.191.219:8001/chain_explorer`:

* **Chain explorer** — `/chain_explorer`. A view of the _chained accounts_ (sequencers,
  delegations, foundries) in the latest reliable branch, with the UTXOs of each chain.
  A good starting point to see which sequencers are running and what delegations exist.
* **DAG explorer** — `/dag_explorer`. An interactive view of the transaction DAG read from
  the node's transaction store: browse by slot, search for a transaction, and inspect a
  transaction together with its past cone.
* **dagviz** — `/dagviz`. A real-time visualizer of the node's in-memory DAG as
  transactions arrive. It needs transaction streaming to be enabled on that node, so it
  may not be available on every access point.
* **Peers** — `/peers`. An auto-refreshing dashboard of the node's peers (static or
  dynamic, alive or dead, round-trip times).

### How to get tokens

You get tokens from a **faucet** with:

```
proxi node getfunds
```

The faucet sends a fixed amount of tokens to the account defined by the `proxi.yaml` in
your current directory. There is a limit of one request per day per account. After
requesting, check your balance with `proxi node balance`; the tokens usually arrive within
15–30 seconds.

The faucet location is configured in `proxi.yaml`:

```yaml
faucet:
    host: 113.30.191.219
    port: 9500
```

> Note: the public faucet is temporarily offline and will be re-enabled shortly. The
> command and configuration above are stable and will not change.

### What can you do with your tokens?

#### Transfer tokens between accounts

Use `proxi node send` to send tokens between accounts (see the [proxi docs](proxi.md)).
For this, `proxi.yaml` must be configured properly — in particular the _tag-along
sequencer_ and _tag-along fee_. List the available sequencers with
`proxi node allchains -q` and pick one as your tag-along.

#### Earn inflation by delegation

Please read [delegation](delegate.md). It is **strongly encouraged** to delegate all but a
small reserve (say `100 or 1000 PROX`) of your tokens as soon as you receive them. List the
sequencers available as delegation targets with `proxi node allchains -q`.

Delegated tokens contribute to the security of the network and, in return, earn you
inflation. Tokens left idle in an ordinary account (an address of the form `a/<hex>`) earn
nothing.

#### Earn inflation by running a sequencer

To run a sequencer you need two things:

1. **An access node.**. This is an ordinary full node that keeps a valid copy of the ledger but
   does not run a sequencer. It is easy to run and needs no tokens. It does not add to the
   consensus security, but it does add to its decentralization by keeping a replica of the
   ledger — the whole network can be recovered from a single node (plus the private keys
   controlling the token accounts). See [Running an access node](run_access.md).
2. **A sequencer** configured on that access node (which then makes it a _sequencer node_).
   Running a sequencer requires tokens. See [Running a node with a sequencer](run_sequencer.md). 
Sequencers generate inflation and so contribute to the network's security on behalf of the
token holder. In addition to the usual inflation, a sequencer may receive a _branch
inflation bonus_.

### Disclaimer

We will do our best to help on the Proxima Discord channel `#testnet`, but please note our
resources are very limited — we count on a growing community that helps each other.

Please also note:

* the tokens are not real and have no value; they are for testing only.
* the Proxima software is experimental at this stage and certainly contains bugs. Do not
  use it in production.
