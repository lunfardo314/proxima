# Multi-spam tool

## Definitions
TPS is _transactions per second_ rate. It is a metric of number of transactions per second coming to the node, to `memdag` and to the ledger.

TPS is _subjective_: each node may perceive TPS slightly differently. The assumption is that average TPS over time are expected to be close across the network.

## Goals

Proxima network should be tested for high TPS situations, at least 100 TPS.
The `multispam` tool is intended to (artificially) create such conditions in the network, normally on a testnet.

The Proxima protocol supports high total throughput of transactions. Same time, it implements measures to prevent DoS-like conditions and attacks.
This makes creating high-TPS conditions not straightforward.

Make a reference implementation for other Proxima automated agents.

### DoS prevention measures

The only type of message exchanged between nodes in the Proxima network is _UTXO transaction_.
Each transaction carries some tokens, therefore is Sybil-protected to a certain extent.
Besides:
- each transaction is signed by exactly one spender of inputs, identified by the _holder ID_, a hash of the public key.
- each transaction is timestamped with the _ledger time_.

Two main DoS-prevention principles in the Proxima protocol:

- _state deposit_ constraint on every UTXO prevents _state bloat_. _State deposit_ requires minimal amount of tokens on the UTXO, that depends on its size in bytes.
See `storageDeposit($0)` EasyFL function in ledger definitions. The constraint is enforced by mandatory lock constraint on each UTXO.
Exceptions are `tagAlong` and `stem` locks.

- TPS is capped individually for each active token holder in the network, i.e. per public key (_holder ID_). The limit is enforced several ways:

  - _pace constraints_ on transactions prevents building dense chains of transactions in the short window of the _ledger time_.
_Pace constraints_ require certain number of ticks distance between consumed UTXO and consuming transaction. It is regulated by the
ledger constants `constTransactionPaceSequencer` for sequencer transactions and `constTransactionPace` for non-sequencer transactions.
  - policy enforced by the `txsenders` core module in the node. It rejects any transaction, that:
     - either has no UTXOs with its _holder ID_ in the ledger state of the _latest reliable branch_
     - or node has already seen a transaction with the same _holder ID_ closer in terms of the timestamp to the one just received, than the required pace.

## Architecture

### Commands
All multispam commands live under `proxi multispam ...`:

- `proxi multispam run` — run multi-spammer with K senders
- `proxi multispam fund` — fund all (or selected) accounts from wallet
- `proxi multispam info` — display account balances and status
- `proxi multispam init` - generates N keys, creates `multispam.yaml` config file with default values 

### Code location
Package `multispam/` in the Proxima repo. Registered as a top-level `proxi multispam` command group (alongside `proxi node`, `proxi wallet`, etc.).

### Configuration
File `multispam.yaml` in working directory.

```yaml
api_hosts:
  - url: "http://localhost:8080"
    timeout: 10s
  - url: "http://localhost:8081"
    timeout: 10s

global:
  transfer_amount: 100000000    # amount to send per transaction (must cover storage deposit)
  finality_timeout_slots: 3     # slots to wait before considering unfinalized tx as failed
  batch_size: 1                 # number of txs per batch (default 1)
  target_strategy: "self"       # "self" | "next" | "random"

senders:
  - name: "sender1"
    key_file: "keys/sender1.key"
  - name: "sender2"
    key_file: "keys/sender2.key"
  - name: "sender3"
    key_file: "keys/sender3.key"
```

**Parameters:**
- `api_hosts` — list of node API endpoints. On failure, rotate to next host.
- `transfer_amount` — tokens to send per transaction. Must be >= storage deposit for target output.
  Self-transfer always occurs via the remainder output.
- `finality_timeout_slots` — how many slots a pending tx can stay unfinalized before its inputs are reclaimed.
- `batch_size` — transactions per batch (chained within pace constraints). Default 1.
- `target_strategy` — where to send the `transfer_amount`:
  - `"self"` — send to own address (two outputs: transfer + remainder, both to self)
  - `"next"` — send to next sender in circular order (1→2→...→K→1)
  - `"random"` — send to a random sender from the active set
- `senders[].key_file` — path to ED25519 keystore file (same format as `proxi` wallet keys)
- `senders[].name` — human-readable label for display

**Derived from ledger / node (not configured):**
- `pace` — taken from ledger constant `constTransactionPace`
- `tag_along_fee` — taken from target sequencer's on-chain data. Default fallback: 1 token.
- `storage_deposit` — computed from `storageDeposit()` for the output size

## Sender Algorithm

Each sender is an autonomous goroutine. Core loop:

### State
- `spentSet`: map of OutputID → txID that spent it, with slot when tx was submitted (normally it is the slot of the txid)
- Current API host index (rotates on failure, also routinely by strategy)

### Main Loop

```
loop:
  1. Query LRB outputs for this account from current API host
     → set of (OutputID, amount) confirmed in current LRB. Once per slot, normally some 1 sec after the slot boudneary 

  2. Classify each output:
     - "available": in LRB AND not in spentSet
     - "pending":   in LRB AND in spentSet, but spending tx IS in LRB (finalized) → remove from spentSet
     - "reclaimable": in LRB AND in spentSet, spending tx NOT in LRB,
                      AND submitted > finality_timeout_slots ago → remove from spentSet, treat as available

  3. Compute available balance = sum of available outputs (after step 2 reclassification)

  4. If available balance < transfer_amount + tag_along_fee + storage_deposit:
     → wait one pace duration, then goto 1

  5. Build transaction(s):
     a. Select inputs from available outputs (enough to cover transfer_amount + tag_along_fee + storage_deposit for remainder)
     b. Produce outputs:
        - target output: transfer_amount to target address (per strategy), with sigLock
        - if it is the last transaction in the batch, tag-along output: fee to a selected active sequencer
        - remainder output: leftover to self, with sigLock
     c. Timestamp: max(input timestamps) + pace ticks
     d. Sign with sender's private key
     e. If batch_size > 1: chain next tx consuming the remainder, repeating from (a)

  6. Submit transaction(s) to current API host
     - On success: add consumed OutputIDs to spentSet with current slot
     - On API error: rotate host, retry once

  7. Record metrics (sent count, timestamp)

  8. Sleep until next pace tick, then goto 1
```

### Key behaviors
- **No inclusion timeout**: sender never blocks waiting for confirmation. It continuously spends whatever is available.
- **Automatic recovery**: if a tx doesn't finalize within `finality_timeout_slots`, its inputs become available again. Double-spends are acceptable and handled by the protocol.
- **Resilient to external interference**: other agents spending from the same account, or unexpected incoming UTXOs, are handled naturally — the sender just works with whatever the LRB shows.
- **Storage deposit**: all produced sigLock outputs must carry at least `storageDeposit` amount. Tag-along outputs are exempt.

## Sequencer Discovery

Global (shared across senders), refreshed once per slot:
1. Query node for active sequencers (from recent slots)
2. For each known sequencer, fetch its on-chain data to get `tag_along_fee`
3. Maintain list of `(chainID, fee)` pairs
4. `NextSequencer()` returns next sequencer round-robin (or random)
5. At least 1 sequencer is assumed. If not, exit 

## Fund Command (`proxi multispam fund`)

Reads `multispam.yaml` and the standard `proxi.yaml` wallet config.

```
proxi multispam fund --amount <tokens> [--sender <name>] [--config multispam.yaml]
```

1. Load wallet private key from `proxi.yaml` (funding source)
2. Load sender list from `multispam.yaml`
3. For each sender (or selected by `--sender`):
   - Derive address from sender's private key
   - Build transfer: wallet → sender address, amount tokens
   - Submit and wait for inclusion in LRB
   - Use remainder from previous tx as input for next (chain funding txs)
4. Display final balances

## Info Command (`proxi multispam info`)

```
proxi multispam info [--config multispam.yaml]
```

For each sender in config:
- Query LRB outputs and balance
- Display: name, holder ID, output count, total balance

## Run Command (`proxi multispam run`)

```
proxi multispam run [--senders K] [--max-duration 10m] [--max-transactions 1000] [--config multispam.yaml]
```

- `--senders K` — use first K senders from config (default: all)
- `--max-duration` — stop after duration (optional)
- `--max-transactions` — stop after total tx count (optional)

## Display

Periodic terminal output (once per slot or every few seconds):

```
[slot 12345] TPS: 47.3 | sent: 1420 | failed: 12
  sender1: sent=480 bal=5.2M  sender2: sent=478 bal=4.8M  sender3: sent=462 bal=5.1M
```

## Implementation Plan

### Step 1: Package skeleton and config parsing
- Create `multispam/` directory
- `multispam/config.go` — parse `multispam.yaml`, validate, derive ledger constants
- Register `proxi multispam` command group in `proxi/`

### Step 2: `proxi multispam init`
- Generate N ED25519 key pairs, save as `.key` files in `keys/` subdirectory
- Generate `multispam.yaml` with default values and sender entries referencing the key files
- Parameters: `--senders N` (number of keys to generate)

### Step 3: `proxi multispam info`
- Load config, load keys, query balances per sender
- Simple display — useful for testing config and connectivity

### Step 4: `proxi multispam fund`
- Transfer from wallet to each sender account
- Chain transactions (each consumes remainder of previous)
- Track inclusion before proceeding to next

### Step 5: Sequencer discovery
- `multispam/sequencer.go` — query active sequencers, cache fees
- `NextSequencer()` function for tag-along target selection

### Step 6: Sender goroutine
- `multispam/sender.go` — the core sender loop as described above
- UTXO tracking, spent set management, transaction building
- Host rotation on API errors

### Step 7: Coordinator and `proxi multispam run`
- `multispam/coordinator.go` — start K senders, collect metrics, display
- Graceful shutdown on Ctrl+C (context cancellation)
- Periodic TPS computation and display

### Step 8: Testing on testnet
- Fund accounts, run with increasing K, observe TPS
- Verify recovery from node failures, double-spends, balance exhaustion

### Reuse from existing code
- `api/client` — all API calls (GetTransferableOutputs, SubmitTransaction, CheckTransactionIDInLRB, etc.)
- `ledger/txbuilder.MakeTransferTransaction` — transaction construction
- `glb.LoadPrivateKeyFromFile` — keystore loading
- `proxi node spam` — bundle building pattern, config reading pattern
