# New transaction validation tests

## Role
Claude is a programmer trying to analyze 

## Goal
We want to write a set of transaction validation tests from scratch, independently on the existing ones

## Requirements
- Create new tests in the separate file `tx_test,go`
- use usual patterns with teh `utxodb`
- plan and implement topics one by one
- ask for clarification and details for each topic one by one 

## Check the following topics:
   
- duplicates not allowed among input IDs
- input commitment prevents "faked UTXO" attack: when upon construction of the transaction a malicious node provides tampered with
UTXOs for UTXO IDs
- signature of the transaction must be valid

The list will be expanded in the future.

