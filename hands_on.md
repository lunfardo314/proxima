# Goal

This file tracks hands-on testing of the single node configuration after the refactoring described in claude_task_library_upgrade.md

The goals:
- is to cover main scenarios while running the node: genesis, normal start/restart/kill, state cleanup, main configuration options
- logging and output
- check and optimize `proxi` tool

---

## Test Scenarios Checklist

### 1. Genesis and Initialization

- [ ] **1.1** `proxi init wallet` - Create new wallet configuration
- [ ] **1.2** `proxi init genesis` - Generate genesis ledger ID and constraints library
- [ ] **1.3** `proxi init genesis_db` - Initialize database with genesis state
- [ ] **1.4** `proxi init bootstrap` - Setup bootstrap sequencer account
- [ ] **1.5** `proxi init node_config` - Generate node configuration template

### 2. Node Lifecycle

- [ ] **2.1** Fresh start - First node start with new genesis database
- [ ] **2.2** Normal restart - Graceful shutdown (`Ctrl+C`) and restart
- [ ] **2.3** Kill recovery - `kill -9` and restart (ungraceful termination)
- [ ] **2.4** Start with corrupted DB - Verify auto-restore from snapshot if available
- [ ] **2.5** Start without DB - Verify auto-restore behavior

### 3. Snapshot and State Management

- [ ] **3.1** `proxi snapshot db` - Create snapshot from running node or DB
- [ ] **3.2** `proxi snapshot info` - Display snapshot file information
- [ ] **3.3** `proxi snapshot check` - Verify snapshot integrity
- [ ] **3.4** `proxi snapshot restore` - Restore DB from snapshot file
- [ ] **3.5** Auto-restore on startup - Verify automatic restore when DB missing/corrupted

### 4. Configuration Options (`proxima.yaml`)

#### 4.1 Peering
- [ ] Host ID private key generation and usage
- [ ] Port configuration
- [ ] Manual peer configuration (known peers)

#### 4.2 API
- [ ] API port configuration
- [ ] API endpoint accessibility

#### 4.3 Sequencer
- [ ] Enable/disable sequencer
- [ ] Sequencer ID configuration
- [ ] Controller key setup
- [ ] Pace setting
- [ ] Max tag-along inputs

#### 4.4 Logger
- [ ] Log level (debug, info, warn, error)
- [ ] Output file configuration
- [ ] Previous log handling (erase/save)
- [ ] Attacher stats logging

#### 4.5 Metrics and Profiling
- [ ] Prometheus metrics port
- [ ] pprof enable/disable and port
- [ ] Trace tags configuration

### 5. Node API Commands (node running)

- [ ] **5.1** `proxi node info` - Get node status information
- [ ] **5.2** `proxi node sync` - Get synchronization status
- [ ] **5.3** `proxi node peers` - List connected peers
- [ ] **5.4** `proxi node balance` - Check account balance
- [ ] **5.5** `proxi node utxos` - List account UTXOs
- [ ] **5.6** `proxi node transfer` - Transfer tokens between accounts
- [ ] **5.7** `proxi node compact` - Compact UTXOs into single output
- [ ] **5.8** `proxi node lrb` - Get latest reliable branch
- [ ] **5.9** `proxi node allchains` - List all sequencer chains
- [ ] **5.10** `proxi node chain` - Get chain output details
- [ ] **5.11** `proxi node ledger_id` - Get ledger ID from node
- [ ] **5.12** `proxi node inactive` - Get inactive chains

### 6. Sequencer Commands (node running)

- [ ] **6.1** `proxi node seq info` - Get sequencer information
- [ ] **6.2** `proxi node seq withdraw` - Withdraw from sequencer
- [ ] **6.3** `proxi node setup_seq` - Setup sequencer chain
- [ ] **6.4** `proxi node mkchain` - Create new sequencer chain
- [ ] **6.5** `proxi node killchain` - Terminate sequencer chain

### 7. Delegation Commands (node running)

- [ ] **7.1** `proxi node delegate amount` - Delegate tokens to sequencer
- [ ] **7.2** `proxi node delegate revoke` - Revoke delegation (askstop)
- [ ] **7.3** `proxi node delegate status` - Check delegation status
- [ ] **7.4** `proxi node delegate submit` - Submit delegation chain

### 8. Faucet Commands

- [ ] **8.1** `proxi node faucet_srv` - Run faucet server
- [ ] **8.2** `proxi node faucet` - Request funds from faucet

### 9. Database Commands (node stopped)

- [ ] **9.1** `proxi db info` - Display database information
- [ ] **9.2** `proxi db tree` - Display trie structure
- [ ] **9.3** `proxi db branches` - List branch transactions
- [ ] **9.4** `proxi db lrb` - Get latest reliable branch from DB
- [ ] **9.5** `proxi db accounts` - List accounts in state
- [ ] **9.6** `proxi db chains` - List sequencer chains
- [ ] **9.7** `proxi db chainstats` - Chain statistics
- [ ] **9.8** `proxi db findtx` - Find transaction in DB
- [ ] **9.9** `proxi db ledger_id` - Get ledger ID from DB
- [ ] **9.10** `proxi db dag` - Generate DAG visualization
- [ ] **9.11** `proxi db ulist` - List UTXOs
- [ ] **9.12** `proxi db analyze_branches` - Analyze branch history
- [ ] **9.13** `proxi db counttx` - Count transactions
- [ ] **9.14** `proxi db upgrades` - Show ledger upgrades

### 10. Transaction Store Commands

- [ ] **10.1** `proxi db txstore get` - Get transaction from store
- [ ] **10.2** `proxi db txstore put` - Put transaction to store
- [ ] **10.3** `proxi db txstore list` - List transactions
- [ ] **10.4** `proxi db txstore idlist` - List transaction IDs
- [ ] **10.5** `proxi db txstore crosscheck` - Cross-check store integrity
- [ ] **10.6** `proxi db txstore past_cone` - Analyze transaction past cone

### 11. Wallet Commands

- [ ] **11.1** `proxi wallet` - Display wallet configuration and status

### 12. Utility Commands

- [ ] **12.1** `proxi util ledger_id` - Parse/display ledger ID
- [ ] **12.2** `proxi util compile_ledger_id` - Compile ledger ID
- [ ] **12.3** `proxi util verify_ledger_id` - Verify ledger ID
- [ ] **12.4** `proxi util parse_tx` - Parse transaction bytes
- [ ] **12.5** `proxi util parse_bytecode` - Parse EasyFL bytecode
- [ ] **12.6** `proxi util decode_msg` - Decode peering message
- [ ] **12.7** `proxi util inflation` - Calculate inflation
- [ ] **12.8** `proxi util hostid` - Generate host ID
- [ ] **12.9** `proxi util private` - Private key operations

### 13. Stress and Performance Testing

- [ ] **13.1** `proxi node spam` - Generate transaction spam for testing
- [ ] **13.2** High TPS handling
- [ ] **13.3** Memory usage under load (pprof)

### 14. Error Handling and Edge Cases

- [ ] **14.1** Invalid configuration file
- [ ] **14.2** Network disconnection during operation
- [ ] **14.3** Database corruption handling
- [ ] **14.4** Conflicting transactions
- [ ] **14.5** Time synchronization issues

---

## Test Progress Log

<!-- Record test results here as tests are performed -->

| Date | Test | Result | Notes |
|------|------|--------|-------|
|      |      |        |       |
