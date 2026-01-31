# New transaction validation tests

## Goal
We want to write a set of transaction validation tests from scratch, independently on the existing ones

## Requirements
- Create new tests in the separate file `tx_test,go`
- use usual patterns with teh `utxodb`

## Check the following:
   
- duplicates not allowed among input IDs
- input commitment prevents "faked UTXO" attack: when upon construction of the transaction a malicious node provides tampered with
UTXOs for UTXO IDs

The list will be expanded in the future