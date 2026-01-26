## TODO big tasks

- ~~refactor ledger commitment to covenants/constraints library. Revisit topic of gradual ledger upgrades with backward compatibility~~
- ~~revisit snapshots and implement periodic state auto-purge~~ ✅ DONE (snapshot_restore module)
- ~~transaction level logging (TxLog)~~
- revisit upper level of transaction structure
- revisit delegation constants, optimized serialization of `amounts`
- compulsory delegation
- revisit and optimize API subsystem v1
- implement API v2, with JSON form of transaction etc
- revisit and optimize `peering`, especially heartbeat protocol
- revisit EasyFL: optimizing, op-code limits, local libraries
- "fair launch" primitives
- test with high TPS and many sequencers (up to 100)
- docs, docs, consistency etc ...

## Less priority
- implement txstore cleanup
- implement IPFS as long-term transaction store
- `amounts` covenant re-implement in EasyFL
- implement richer and more flexible crypto op-codes. E.g. add BLS
- migrate to RocksDB
