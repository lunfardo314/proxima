# One-Node Bootstrap: Manual Testing Scenario

## Purpose

Step-by-step manual test of the full Proxima bootstrap-from-genesis flow on a single node.
Covers key generation, wallet/node initialization, genesis creation, node startup, and a
comprehensive set of `proxi` CLI commands against the running node.

Should cover different config options (keys, logger, sequencer) in both node adn the proxi wallet.

Use tracing and txlogging facilities, if needed.

All strange behavior and deviations from expected results should be logged in the
**Issues Log** section at the bottom. Also log unclear, inconsistent or too verbose output.

**Rule**: Document all UX-related findings as issues, even if they are not bugs (e.g. confusing
output, missing feedback, inconsistent formatting, unclear error messages).

## Prerequisites

- `proxima` and `proxi` binaries are on the PATH
- A fresh empty directory (ask the operator for the directory name)
- Two terminal sessions (one for the node, one for proxi commands)

---

## Phase 1: Setup

### 1.1 Create working directory

```bash
mkdir <dirname> && cd <dirname>
```

### 1.2 Copy startup script

```bash
cp <path-to-repo>/run_proxima.sh .
```

### 1.3 Generate private key (unencrypted)

```bash
proxi util key generate
```
Expected: creates `proxima.key` with `"private_key"` field visible in JSON.

### 1.4 Verify key info

```bash
proxi util key info --file proxima.key
```
Expected: displays key type (ED25519), spender ID, public key.

### 1.5 Encrypt the key

```bash
proxi util key encrypt --hint "test hint"
```
Expected: prompts for passphrase twice, overwrites `proxima.key` with `"crypto"` field.

### 1.6 Verify encrypted key info

```bash
proxi util key info --file proxima.key
```
Expected: shows encrypted status, hint, spender ID, public key. No private key displayed.

### 1.7 Initialize wallet profile

```bash
proxi init wallet
```
Expected: creates `proxi.yaml` with wallet section referencing `proxima.key`.

### 1.8 Verify wallet config

```bash
proxi wallet
```
Expected: displays wallet configuration from `proxi.yaml`.

### 1.9 Initialize node profile with bootstrap sequencer

```bash
proxi init node -b
```
Expected: creates `proxima.yaml` with peering, API, and sequencer sections.
Sequencer section should have `name: boot`, `enable: true`, bootstrap chain ID filled in.

### 1.10 Adjust node profile

Edit `proxima.yaml` manually:
- Enable txlogger:
```yaml
txlogger:
  enable_on_start: true
  level: "all"
  ttl_hours: 1
  enable_on_off_api: true
```

### 1.11 Initialize genesis

```bash
proxi init genesis
```
Expected: prompts for confirmation, creates a `.snapshot` file in the current directory.
Note the bootstrap sequencer ID displayed.

### 1.12 Snapshot placement

The genesis `.snapshot` file can stay in the node's working directory (no need to move it to a `snapshot/` subdirectory).

---

## Phase 2: Start the Node

### 2.1 Start the node (Terminal 1)

```bash
./run_proxima.sh
```
If the key is encrypted, the script prompts for the passphrase.
Expected: node starts, sequencer begins producing branches. Log output in `proxima.log`.

### 2.2 Wait for first branches

Wait ~30 seconds for the sequencer to produce a few branches before running commands.

---

## Phase 3: Node Information Commands (Terminal 2)

All commands below are run from the working directory in Terminal 2.
If the key is encrypted, either set `PROXIMA_KEY_PASSPHRASE=<passphrase>` or create
a passphrase file named after the spender ID (hex, no extension) in the working directory.

### 3.1 Node info

```bash
proxi node info
```
Expected: displays node ID, sequencer ID, LRB info, ledger constants.

### 3.2 Latest reliable branch

```bash
proxi node lrb
```
Expected: shows LRB branch ID, supply, coverage, healthy status.

### 3.3 Sync status

```bash
proxi node sync
```
Expected: shows sync status of the node.

### 3.4 Peer info

```bash
proxi node peers
```
Expected: shows connected peers (none for single-node setup).

### 3.5 Latest sequencer milestones

```bash
proxi node last_seq
```
Expected: lists latest known sequencer milestones.

### 3.6 All chains

```bash
proxi node allchains
```
Expected: lists all chains in the latest reliable branch. Should show at least the bootstrap chain.

---

## Phase 4: Wallet and Account Commands

### 4.1 Account balance

```bash
proxi node balance
```
Expected: shows total balance controlled by the wallet account, including the sequencer chain.

### 4.2 List UTXOs

```bash
proxi node utxo
```
Expected: lists all outputs locked to the wallet account.

### 4.3 Chain details

```bash
proxi node chain <bootstrap-chain-id>
```
Use the bootstrap chain ID from genesis output. Expected: shows chain details, amount, lock.

### 4.4 Get chain output

```bash
proxi node get_chain_output <bootstrap-chain-id>
```
Expected: returns the chain output data for the bootstrap sequencer.

---

## Phase 5: Transaction Commands

### 5.1 Withdraw from sequencer

```bash
proxi node seq withdraw 100000000
```
Expected: single passphrase prompt (if encrypted), transaction submitted,
tracked to inclusion depth 1, txlog displayed.

### 5.2 Verify balance after withdraw

```bash
proxi node balance
```
Expected: non-chain outputs should now include the withdrawn amount.

### 5.3 Transfer to self

```bash
proxi node transfer 50000000 -f
```
Expected: transaction submitted and included. Creates a new output.

### 5.4 Compact outputs

```bash
proxi node compact -f
```
Expected: multiple non-chain outputs are compacted into one.

### 5.5 Verify compaction

```bash
proxi node utxo
```
Expected: should show fewer non-chain outputs than before compaction.

### 5.6 Second withdraw (larger amount)

```bash
proxi node seq withdraw 500000000 -f
```
Expected: successful withdraw, included at target depth.

---

## Phase 6: Transaction Logger Commands

### 6.1 Txlog status

```bash
proxi node txlog status
```
Expected: shows txlogger is enabled.

### 6.2 Txlog tail

```bash
proxi node txlog tail
```
Expected: shows recent transaction log entries.

### 6.3 Disable txlog

```bash
proxi node txlog disable
```
Expected: txlogger disabled confirmation.

### 6.4 Enable txlog

```bash
proxi node txlog enable
```
Expected: txlogger re-enabled.

---

## Phase 7: Chain Management

### 7.1 Create a new chain

```bash
proxi node mkchain 100000000 -f
```
Expected: creates a new chain origin. Transaction included.

### 7.2 Verify new chain exists

```bash
proxi node allchains
```
Expected: should now list the bootstrap chain plus the newly created chain.

### 7.3 List wallet chains

```bash
proxi node balance
```
Expected: should show the new chain in the non-delegation chains section.

### 7.4 Kill the new chain

Use the chain ID from step 7.1:
```bash
proxi node killchain <new-chain-id> -f
```
Expected: destroys the chain, converts tokens to a regular ED25519 output.

### 7.5 Verify chain destroyed

```bash
proxi node allchains
```
Expected: the killed chain should no longer appear.

---

## Phase 8: Delegation Commands

### 8.1 Create delegation by amount

```bash
proxi node delegate amount 100000000 -f
```
Expected: creates a delegation chain output to the default sequencer. Transaction included.

### 8.2 Check delegation status

```bash
proxi node delegate status
```
Expected: lists active delegations controlled by the wallet.

### 8.3 Request delegation stop

Use the delegation chain ID from step 8.2:
```bash
proxi node delegate askstop <delegation-chain-id> -f
```
Expected: sends stop request to the sequencer.

---

## Phase 9: Node Restart

### 9.1 Stop the node

In Terminal 1, press Ctrl+C to stop the node. Wait for graceful shutdown.

### 9.2 Restart the node

```bash
./run_proxima.sh
```
Expected: node restarts from the persisted database. Sequencer resumes producing branches.

### 9.3 Verify state persisted

```bash
proxi node balance
```
Expected: balance should be consistent with the state before shutdown (plus any new inflation).

### 9.4 Verify UTXOs persisted

```bash
proxi node utxo
```
Expected: outputs should match pre-shutdown state.

### 9.5 Submit transaction after restart

```bash
proxi node transfer 50000000 -f
```
Expected: transaction submitted and included normally after restart.

---

## Phase 10: Sequencer Info

### 10.1 Sequencer info

```bash
proxi node seq info <bootstrap-chain-id>
```
Expected: displays sequencer details.

---

## Phase 11: Inactive Outputs

### 11.1 Get inactive outputs

```bash
proxi node get_inactive
```
Expected: displays UTXOs inactive since the default lookback period.

---

## Phase 12: Database Commands (node must be stopped)

### 12.1 Get ledger definitions from node

```bash
proxi node get_ledger_definitions
```
Expected: saves `proxima.genesis.definitions.yaml` file.

### 12.2 Get ledger definitions from DB

```bash
proxi db get_ledger_definitions
```
Expected: saves definitions file from database.

### 12.3 Database info

```bash
proxi db info
```
Expected: displays general info about the state database.

### 12.4 Database LRB

```bash
proxi db lrb
```
Expected: shows LRB from database perspective.

### 12.5 Database branches

```bash
proxi db branches
```
Expected: shows branch records.

### 12.6 Database accounts

```bash
proxi db accounts
```
Expected: lists accounts and their totals from state DB.

---

## Cleanup

```bash
# Stop the node (Ctrl+C in Terminal 1)
# Optionally remove the working directory
rm -rf <dirname>
```

---

## Issues Log

Record any unexpected behavior, errors, or deviations from expected results below.

| Step | Command | Expected | Actual | Notes |
|------|---------|----------|--------|-------|
| 1.3 | `proxi util key generate` | Clear prompt | Blocks waiting for stdin with no output visible when run non-interactively | UX: silent block, exit code 1 with no error when stdin closes |
| 1.7 | `proxi init wallet` | Confirmation message | Silent success, no output | UX: should print what was created |
| 1.7 | `proxi init wallet` | API endpoint for local node | Defaults to testnet `63.250.56.190:8001` | UX: port mismatch with node template (8000 vs 8001) |
| 1.7 | `proxi init wallet` | `sequencer_id` auto-filled | Placeholder `<own sequencer ID>` | UX: should auto-detect from `proxima.yaml` if `-b` was used |
| 1.10 | `proxima.yaml` template | txlogger enabled | txlogger section commented out by default | Minor: test doc says to enable manually |
| 3.2 | `proxi node lrb` | `0.00%` | `0.00%%` (double percent) | Bug: format string has `%%` |
| 3.4 | `proxi node peers` | "no peers connected" | Blank output after header | UX: no explicit message for zero peers |
| 5.1 | `proxi node seq withdraw` | Works | "can't get own sequencer id" | Config issue: wallet `sequencer_id` placeholder not set |
| 6.4 | `proxi node txlog enable` | Restores configured level (`all`) | Re-enables with `non_sequencer` | Bug/UX: doesn't restore original config level |
| 7.1 | `proxi node mkchain` | Transaction included | Never included, tag-along outputs purged from backlog | **BUG FIXED**: MakeChainOrigin() was missing SubmitTransaction() call |
| 8.1 | `proxi node delegate amount` | Works | "selecing" in output | UX: typo "selecing" → "selecting" |
| 8.3 | `proxi node delegate askstop` | Transaction included | Rejected: "compensation not sufficient (needed X, provided Y)" where Y > X | **BUG FIXED**: comparison was backwards (`<` instead of `>`) in req_askstop.go:95 |
| 12.3 | `proxi db info` | DB info displayed | "yaml: control characters are not allowed" | **BUG**: crash/error |
| 12.4 | `proxi db lrb` | Formatted output | All info on single line, unreadable | UX: poor formatting |
| 12.4 | `proxi db lrb` | `0.00%` | `0.00%%` | Bug: same double-percent as node lrb |
| 12.6 | `proxi db accounts` | `100%` | `100%%` | Bug: same double-percent issue |

