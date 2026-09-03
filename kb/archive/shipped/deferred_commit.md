## Optimization of branch commit

### Current architecture

For each branch transaction, the attacher wraps up by committing delta of the ledger state to DB. It also writes root record with respective root commitment in the trie.
See `wrapup.go`, `commitBranch()`

### The problem
For the same slot, many branches can be produced by sequencers and all of them are committed to DB. 
However, ultimately only one of those branches survives while the rest are essentially orphaned and becomes garbage in DB.
It is not immediately clear which one survives, because cooperative consensus is probabilistic.

### Goals
Defer the commitment of the DB mutation until the moment when the ledger state corresponding to the branch is really used (lazy commitment).
Until then, branch commitment must be pending with all data produced by the attacher.  
The branch is 'really used' whenever corresponding ledger state reader for the branch is requested from the `Branches` module via a `GetStateReaderForTheBranch()` call.
Upon request for the ledger state that is pending, it should be committed tp the DB.

Those pending branches that states are never requested should be cleaned up after the corresponding branch vertex will be deleted from the memdag and GC-ed.
The corresponding mutations of unrequested branches should not reach database.

### Implementation guidelines

All the DB update logic currently implemented as :
```go
	err := upd.Update(muts, &multistate.RootRecordParams{
		StemOutputID:    stemOID,
		SeqID:           seqID,
		CoverageDelta:   *a.finals.CoverageDelta,
		FrozenCoverage:  *a.finals.FrozenCoverage,
		SlotInflation:   *a.finals.SlotInflation,
		Supply:          *a.finals.Supply,
		NumConfirmedTransactions: uint32(a.finals.MutationStats.NumConfirmedTransactions),
	})
```
must be moved to the Branches module. 

The pending branch can preserve `attachFinals` data structure beyond the attacher. The attacher will usually be finished by then.

Note, that some data used by code and attacher, such as root commitment, will become known only after commitment to the trie.
It seems, this data is only used for txmetadata, consistency checking and logging, therefore not critical.
It should be carefully refactored to avoid unnecessary code dependencies.

Logging in the main log and in the txlog:
- after attacher finishes, as currently
- when branch is committed
- when and if branch is orphaned/GC-ed

