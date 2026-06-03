## CLI wallet program `proxi`

`proxi` is a small command-line program with basic tools for Proxima. It works as a
wallet (sending and receiving tokens, looking at balances) and as an administration
tool for a node.

You can compile it by typing `go install` in the `<root>/proxi` directory of the
Proxima source. The resulting program is called `proxi`.

Typing `proxi -h`, `proxi node -h`, `proxi db -h`, and so on, displays help text for
each group of commands.

Most commands need a *wallet profile* in the working directory (see below). A few
commands, such as `proxi util key generate`, are completely stand-alone and do not
need a profile.

The wallet profile is usually a file named `proxi.yaml`. If you use a file with a
different name, say `proxi2.yaml`, you tell `proxi` about it with the `-c` flag and
the profile name without the `.yaml` extension, for example
`proxi node balance -c proxi2`.

Command `proxi wallet` displays the main parameters of the profile.
Command `proxi version` displays build information.

Most `proxi` commands have the form `proxi <group> <subcommand> <arguments and flags>`,
where `<group>` is one of:

* `proxi config` — creates wallet and node configuration profiles
* `proxi init` — initializes a node database (genesis)
* `proxi util` — small helper commands (key generation, parsing, and similar)
* `proxi db` — commands that read or change the node's database files **directly**,
  bypassing the node. They will fail if the node is running. Direct access to the
  database may also change file permissions, so the node might fail to open the
  database afterwards if it runs under a different user.
* `proxi snapshot` — commands for working with state snapshots
* `proxi node` — commands that talk to a running node over its API. They all need a
  configuration profile and the address of a running node.

### 1. Create a wallet profile

The command `proxi config wallet` creates a new wallet. It:

* asks you to type at least ten random characters, which it mixes with system
  randomness to generate a private key;
* saves the private key into a separate **key file** (a `.key` file). You may
  optionally protect this file with a passphrase;
* creates the profile file `proxi.yaml`.

By default the profile is named `proxi`. You can pass a different name, for example
`proxi config wallet myname`, which creates `myname.yaml`.

The generated `proxi.yaml` looks like this (with explanatory comments; the
`holder_id` value below is just an example — yours will differ):

```yaml
# default sequencer ID is used when own or tag-along sequencer is not specified
default_sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82

wallet:
    key_file: proxi.key
    holder_id: dcc2f3be5c019d15108d6169d3f826ac20c73a31db8ad5c5d58e9ab01d3a903a
    # sequencer_id is the sequencer ID controlled by the private key of the wallet.
    # The controller wallet can withdraw tokens from the sequencer chain with command
    # 'proxi node seq withdraw'
    sequencer_id: 9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82
api:
    endpoint: http://127.0.0.1:8000

tag_along:
    # tag-along fee amount and ID of the tag-along sequencer
    fee: 1
#    sequencer_id: <tag-along sequencer ID>
```

**You usually need to adjust the profile before using it** — in particular the API
endpoint.

`wallet.key_file` is the name of the key file that holds the private key. Keep this
file secret. The private key is **not** stored in `proxi.yaml`.

`wallet.holder_id` is the identifier of the wallet account. It is the hash of the
wallet's public key. The node uses it as a consistency check against the key file.

`default_sequencer_id` is used whenever a tag-along or own sequencer ID is not given
explicitly. `proxi config wallet` sets it to
`9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82`, which is the ID of the
*bootstrap sequencer* (a built-in constant).

`wallet.sequencer_id` matters only if you run a sequencer. It is the ID of the
sequencer controlled by this wallet's private key, needed for the
`proxi node seq withdraw` command. It defaults to the bootstrap sequencer ID.

`api.endpoint` must contain the address of a node's API, in the form
`http://<host>:<port>`. The generated profile points at a local node
(`http://127.0.0.1:8000`). **Change it to the address of the node you want to use** —
for example a public access node of the testnet.

`tag_along.fee` and `tag_along.sequencer_id` describe the *tag-along* mechanism, which
every token-sending command relies on. Each transaction you send carries a small extra
output, the **tag-along output**, that pays `tag_along.fee` tokens to the sequencer in
`tag_along.sequencer_id` (defaulting to `default_sequencer_id`). That sequencer
consumes the tag-along output in its own transaction, which is what pulls your
transaction into the ledger. Without this, your transaction would not be picked up.
Common configuration mistake is wrong, non-existent tag-along sequencer ID. In that case `proxi` creates
transaction that is tagged-along the non-existent sequencer, so nobody picks it.
Everything looks well but transaction does not confirm (is not included into the ledger state).

### How to read transaction and other IDs

A **transaction ID** is 32 bytes. The first 5 bytes are the transaction's timestamp.
The 6th byte holds the number of outputs the transaction produced, minus one. The
remaining 26 bytes are taken from the `blake2b` hash of the transaction.

A transaction ID is shown in a *dashed* form, for example:

```
s58514-30-a1b2c3...
58579-25-0b55...
```

The first number is the slot (decimal). The second number is the number (decimal) of ticks within the
slot. The rest is the hex-encoded hash part.

A leading `s` means the transaction is a **sequencer transaction**. A sequencer
transaction whose tick count is `0` sits exactly on a slot boundary and is called a
**branch transaction** (for example `s58565-0-...`) and it represents a committed ledger state. A transaction with no `s` prefix
is an ordinary transaction produced by a user wallet.

An **output** (also called a UTXO) belongs to the transaction that produced it and has
an index from 0 to 255 within that transaction. The **output ID** or **UTXO ID** if made from the transactions ID by appending index in the end.
The index is shown after the transaction ID with a `#`, for example:

```
s58579-25-010b55...#3
```

is output number 3 of that transaction.

A **chain ID** is 24 bytes, shown in hex sometimes with a `$/` prefix, for example
`$/6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7`. Chain ID identifies a **chained account**, a cornerstone concept of Proxima's UTXO ledger.

An ordinary **address**, an ID of a conventional (`sigLock`) account known in crypto is shown as `a/<holder ID hex>`, for example
`a/370563b1f08fcc06fa250c59034acfd4ab5a29b60640f751d644e9c3b84004d0`. 
The hex part contains the hash of the type of the signature and public key. It is called **holder ID**.
Addresses are used in the `sigLock` spending condition of the UTXO.

Chain IDs may also be used in `chainLock` UTXO spending condition, that is a spend target different from usual `sigLock`. 
In tha case it is written as `c/<chain ID hex>`.

Both forms may be used as send targets in the `proxi node send` and `proxi node seq withdraw` commands with the flag `-t` or `--target`
- `-t a/<holder ID hex>` means tokens are sent to the address. These will be spendable with the private key of the holder
- `-t c/<chain ID>` means tokens in the UTXO are sent to the chain and will belong to the _chained account_. The private key that
controls the chained account will be able to spend tokens.

If the `-t` flag is omitted, the target defaults to the wallet's own account.

### Some useful `proxi node` commands

* `proxi node lrb` displays information about the **latest reliable branch (LRB)**. The
  LRB is the ledger state that all current healthy ledger states agree on — in other
  words, the **consensus ledger state**, with high probability. Usually you want your
  transaction to be included in the LRB or an earlier branch.

  If the LRB is more than a few slots behind the current time, the node may be out of
  sync with the network.

  The LRB information also shows the sequencer that produced the branch, the total
  supply of tokens in that ledger state, and its **ledger coverage**. For a branch to
  be considered *healthy*, its ledger coverage must be larger than the supply; the
  maximum possible coverage is twice the supply.

* `proxi node balance` displays the token balance of the wallet's account in the LRB —
  both on the ordinary address and on chains controlled by the account. The balance is
  the sum of tokens in ordinary outputs plus the balances held in chains. The command
  also lists any *delegations* to sequencers.

* `proxi node send <amount> -t "<target>"` sends tokens from the wallet to a target.
  The target is an address (`a/<hex>`) or a chain (`c/<hex>`). For example:

  ```
  proxi node send 1000 -t "a/370563b1f08fcc06fa250c59034acfd4ab5a29b60640f751d644e9c3b84004d0"
  ```

  sends 1000 tokens. The transaction includes the tag-along output described above.
  The global `-v` (verbose) flag makes the command print the whole transaction, which
  is a good way to get acquainted with Proxima's transaction model. Run
  `proxi node send -h` to see advanced options (such as a deadline or an attached tag).

* `proxi node compact [<max inputs>]` gathers up to `<max inputs>` of the account's
  outputs into a single output by sending them to yourself. This is useful when the
  account has accumulated many small outputs and you want to reduce the storage they
  occupy. The default is 100 inputs and the maximum is 256. Like any send, this still
  requires a tag-along fee.

* `proxi node utxo` lists the outputs (UTXOs) in the account. Add the global `-v` flag
  to show the parsed contents of each output.

* `proxi node info` displays information about the node.

Beyond these, `proxi node` has commands for chains (`mkchain`, `killchain`, `chain`,
`allchains`), delegation (the `proxi node delegate` group), native tokens
(`proxi node foundry`), sequencer control (`proxi node seq`), peers and sync status,
and more. Run `proxi node -h` to see the full list, and `proxi node <command> -h` for
the details of each.

### Delegation

See [delegation](delegate.md).
