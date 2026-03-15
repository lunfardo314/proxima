# Bottleneck Investigation and Optimization

## Goal
Identify and remove performance bottlenecks in the Proxima node to improve throughput, reduce latency, and increase stability under load.

## Approach
1. **Profile with pprof** — collect CPU, mutex contention, and memory profiles from running testnet nodes to identify actual bottlenecks with data
2. **Optimize sequencer algorithms** — targeted fixes based on profiling findings
3. **Evaluate RocksDB** — replace BadgerDB if DB I/O is identified as a bottleneck

## Phase 1: pprof Profiling

### Setup
- pprof is already in the node: `startPProfIfEnabled()` in `node/node.go`
- Enable via `proxima.yaml` metrics/pprof config
- Testnet machines: `boot` (113.30.191.219), `loc0` (63.250.56.190), `seq1` (83.229.84.197), `loc1` (5.180.181.103)

### Profiles to Collect
- **CPU profile** (30s): `go tool pprof http://<host>:<pprof_port>/debug/pprof/profile?seconds=30`
- **Mutex contention**: `go tool pprof http://<host>:<pprof_port>/debug/pprof/mutex`
- **Block profile**: `go tool pprof http://<host>:<pprof_port>/debug/pprof/block`
- **Goroutine dump**: `http://<host>:<pprof_port>/debug/pprof/goroutine?debug=2`
- **Heap profile**: `go tool pprof http://<host>:<pprof_port>/debug/pprof/heap`

### What to Look For
- Hot functions in CPU profile (trie operations, hashing, serialization)
- Mutex contention hotspots (branches.mutex, ownMilestonesMutex, vertex locks)
- Goroutine counts and states (blocked on channels, mutexes, I/O)
- Memory allocation patterns (GC pressure)

### Findings (2026-03-15, boot node under multispam load)

#### CPU Profile (30s, 186% CPU = ~2 cores)

**#1 bottleneck: `TransactionID.StringShort` / `fmt.Sprintf` — 43% of CPU**
- `PastConeBase.Lines()` → `IDShortString()` → `StringShort()` → `fmt.Sprintf` → `doPrintf`
- Called from `MergePastCone` during `attachVertexNonBranch` in every `IncrementalAttacher`
- **60% of CPU** is in `attachVertexNonBranch` → `MergePastCone` → `Lines()`
- Pure string formatting overhead — likely debug/trace logging that builds strings even when not printed
- **Fix**: Lazy string formatting, avoid `Lines()` calls unless logging is enabled

**#2: Queue inputLoop contention — ~7% of CPU**
- Multiple queues (events, txinput, txsenders, poker) spending CPU in `inputLoop`
- Channel send/receive overhead

**#3: Memory allocation — 25% of CPU in `mallocgc`**
- Heavy allocation from `fmt.Sprintf`, string building, past cone operations

#### Mutex Profile (268s total contention)

**#1: `Readable.mutex` (RWMutex) — 92% of mutex contention**
- `sync.(*RWMutex).Unlock` = 52% flat, `sync.(*Mutex).Unlock` = 40% flat
- **All from sequencer proposers** (90.5% cumulative under `proposer.run`)
- Hot path: `ChooseFirstExtendEndorsePair` (85%) → `IncrementalAttacher` (47%) → `attachVertexNonBranch` (44%)
- Key callers: `GetUTXO` (23%), `GetUTXOForChainID` (11%), `WrappedTx.Unwrap` (50%)
- **Root cause**: Proposers share cached `Readable` state readers via `branches.stateReaders`. The `Readable.mutex` uses exclusive `Lock()` for trie reads (trie reader mutates internal cache). Multiple proposers compete for the same `Readable` instance.

**#2: `WrappedTx.Unwrap` — 50% cumulative**
- Per-vertex RWMutex contention as proposers unwrap same vertices concurrently

**#3: `GetChainOutputFromBranch` — 15% cumulative**
- Goes through `branches.mutex` to walk pending branches

#### Block Profile (31 hours cumulative blocking)

- 73% in `runtime.selectgo` — goroutines idle in select loops (normal)
- 17% in QUIC stream reads — peering I/O (normal)
- 6.5% in BadgerDB compaction — background DB maintenance
- No pathological blocking found

#### Summary of Bottlenecks (priority order)

1. **String formatting in hot path**: `PastConeBase.Lines()` called during `MergePastCone` burns 43% CPU on `fmt.Sprintf` for `TransactionID.StringShort`. This is likely trace/debug logging.
2. **Shared `Readable.mutex`**: All proposers compete for the same cached state reader's exclusive lock. Each proposer should get its own `Readable` instance.
3. **`WrappedTx` vertex lock contention**: Proposers unwrap same vertices. Consider read-only access pattern or caching unwrapped data.
4. **Memory allocation pressure**: 25% CPU in GC, driven by string formatting allocations.

---

## Phase 2: Sequencer Algorithm Optimization

### Known Issues (from deadlock investigation, 2026-03-15)

#### Fixed: Lock convoy in branches.mutex / ownMilestonesMutex
- **Root cause**: `_commitPendingBranch` held `branches.mutex` during slow trie GC (`PrunableTxIDsAtSlot`). `IsConsumedInThePastPath` held `ownMilestonesMutex` while waiting for `branches.mutex`. Cascaded to block entire sequencer loop.
- **Fix**: Moved branch commit outside mutex (commit `24c842c6`). Changed `IsConsumedInThePastPath` to RLock/unlock/IO/Lock pattern.

#### Suspect: insertTagAlongInputs per-output trie queries
- `proposal.go:105-108`: `PurgeSlice` calls `IsConsumedInThePastPath` for each tag-along candidate
- Each call potentially hits the trie via `getStateReader().OutputIsConsumed()`
- Could batch these queries or cache results more aggressively

#### Suspect: Multiple proposer goroutines contending
- `task.go:209`: Multiple proposers started per sequencer step
- All compete for `ownMilestonesMutex` and `branches.mutex`
- Sequencer loop blocks on `WaitGroup.Wait` until all finish

### Areas to Investigate (pending pprof data)
- Proposer parallelism: is it beneficial or just causing contention?
- Tag-along input filtering: batch vs per-output state queries
- Past cone solidification: attacher goroutine scaling
- Trie cache effectiveness: `clearCacheAtSize` tuning

### Fixed: Eager Lines() in MergePastCone assert (commit `57788d11`)
- `pcb.Lines("    ").String` in `past_cone.go:686` was evaluated eagerly in every iteration of `MergePastCone`
- 43% of CPU under load, 60% cumulative — pure string formatting waste
- Fix: wrapped in `func() string { ... }` closure for lazy evaluation via `lazyargs.Eval`
- **Result: 3.6x CPU reduction** (56s → 15.5s samples in 30s without load; 56s → 44s under spammer)

### Confirmed: Shared `Readable.mutex` is the #1 remaining bottleneck
- After Lines fix, pprof under spammer shows:
  - **79% of mutex contention** in `Readable.GetUTXO` (was masked by Lines overhead before)
  - **72% cumulative** from `CoverageDeltaRaw` → `GetOutputFromStateReader` → shared `Readable`
  - **45% of CPU** in trie reads (`TrieReader.Get` → `FetchNodeData` → BadgerDB `Get`)
  - **23% of CPU** in BadgerDB `Get` calls
- Root cause: all proposers use the same cached `Readable` from `branches.stateReaders`. The `Readable.mutex` takes exclusive `Lock()` for every trie read (trie reader mutates internal cache). Proposers are serialized.
- **Next fix**: give each attacher its own `Readable` instance to eliminate contention

### Fixed: Per-proposer Readable (commit `f243ba3f`)
- `GetVirtualStateReaderForTheBranch` now creates a fresh `Readable` per call instead of returning the shared cached instance
- Eliminated `Readable.mutex` contention entirely (was 52-55% of all mutex contention)
- Total mutex contention dropped from 178s → 11.8s (**15x reduction**) without load
- Under spammer: `RWMutex` contention went from 140s → **0s**

### Current state after all fixes (under spammer load)
- **CPU**: 59s samples in 30s (196% = ~2 cores). Was 56s before but now doing real work, not string formatting
- **Top CPU**: `TrieReader.traverseImmutablePath` (63%), `FetchNodeData` (58%), BadgerDB `Get` (38%)
- **Mutex contention**: 149s total, **100% inside BadgerDB** (`oracle.readTs` — transaction timestamp allocation)
- **No Proxima-level mutex contention remains**
- The bottleneck is now BadgerDB's per-read transaction overhead: each trie node fetch creates a `NewTransaction` → `readTs` lock

### Next optimization targets
1. **BadgerDB transaction batching**: batch multiple trie node reads into a single transaction to reduce `oracle.readTs` contention
2. **Trie cache tuning**: fresh Readable starts with empty cache; consider pre-warming or larger cache limits
3. **RocksDB evaluation**: if BadgerDB internal locking remains the bottleneck

---

## Phase 3: RocksDB Evaluation

### Motivation
- BadgerDB's `oracle.readTs` mutex is now the #1 remaining bottleneck under load
- Each trie node fetch creates a new BadgerDB read transaction → timestamp allocation lock
- RocksDB doesn't have this per-read transaction overhead for simple gets

### Architecture
- KV store is abstracted via `unitrie/common.KVReader` / `common.KVStore` interfaces
- Current adapter: `unitrie/adaptors/badger_adaptor`
- New adapter would be: `unitrie/adaptors/rocks_adaptor` (or similar)
- Swap point is in `node/db.go` where `badger_adaptor.MustCreateOrOpenBadgerDB` is called

### Trade-offs
- **Pro**: Better read performance, more mature compaction, wider industry adoption
- **Con**: CGo dependency (complicates cross-compilation), larger binary, different tuning knobs
- **Decision**: Only pursue if pprof shows DB I/O as a significant bottleneck

### Status
_(pending Phase 1 results)_

---

## Changelog

| Date | Change | Commit |
|------|--------|--------|
| 2026-03-15 | Fix lock convoy: branch commit outside mutex, IsConsumedInThePastPath lock restructuring | `24c842c6` |
| 2026-03-15 | Unify snapshot directory, update startup restore behavior | `87356057` |
| 2026-03-15 | Enable mutex/block profiling when pprof is active | `09dfe099` |
| 2026-03-15 | Fix eager Lines() in MergePastCone assert — 3.6x CPU reduction | `57788d11` |
| 2026-03-15 | Per-proposer Readable — eliminate Readable.mutex contention (15x reduction) | `f243ba3f` |
