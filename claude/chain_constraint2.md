## Refactoring of `chain` constraint

### Current
`chain` constraint is defined in `def/chain.easyfl`. 
Currently, in `chain($0,$1,$2,$3)`:
- $0 - chain ID
- $1 - predecessor input index (1 byte, or 0x empty for origin)
- $2 - origin slot
- $3 - origin amount

### Goal

Change arguments:
- $0 and $1 remain untouched
- _origin amount_ is no longer needed. Instead, $3 must be enforced to _cumulative chain inflation_. 
Constraint must enforce (only in _produced_ context) $3 equal to sum of $3 on predecessor and _chain inflation_ in this output.
_Chain inflation_ is equal to _inflation amount_ on non-branch output and _inflation amount_ minus _branch inflation bonus_ on branch transaction.
- $4 must be enforced to _cumulative branch inflation bonus_. It will be non-zero only on sequencer chains.
- $5 must be incremental counter: it must be enforced $5 equal $5 on predecessor plus 1
- $3, $4 must be encoded as EasyFL `z64/` literals
- $5 must be encoded as EasyFL `z32/` literal
- $3, $4, $5 must be `0x` (zero value) at chain's origin

Serialization, tests and other  infrastructure must be adjusted accordingly. 
Tests for new $3, $4, $5 must be added
