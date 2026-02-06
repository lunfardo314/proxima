# New transaction validation tests

## Goal
Write a set of validation tests for the Proxima part related to ledger and transactions, mostly located in `/ledger`. 

Similar set of test already exists in the `/ledger/tests`, however the goal of this task is to revisit the codebase independently,
check its consistency, detect possible vulnerabilities and attack vectors. 

The new set of tests must contain proofs that potential (theoretically possible) attack vectors are not possible. 

## Requirements

- analyze code and [available docs](https://lunfardo314.github.io/#/txdocs/tx)
- ask clarifying questions one by one, do not overwhelm me.
- keep log and state between sessions in the `tx_test.md`
- Create new tests in the separate file `tx_test.go`. If file grows too big, split it into several logical parts
- do not modify edit existing code. Detected vulnerabilities and problems must be documented in the `tx_test.go` and only fixed upon request 
- in tests, use usual patterns with the `utxodb`
- start with basic tests. 
- Many of validity rules are encoded as _EasyFL_ covenants. Expect additional instructions when writing tests for separate covenants. 

## In tests, cover the following topics
   
- duplicates not allowed among input IDs
- input commitment prevents "faked UTXO" attack: when upon construction of the transaction a malicious node provides tampered with
UTXOs for UTXO IDs
- signature of the transaction must be valid
- edge cases of the basic validation
- propose important topics

The task is incremental, the list to be expanded in the future.

