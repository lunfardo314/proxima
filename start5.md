**--  in progress  --**

# Instruction: how to run a testnet with 5 sequencers

## Initialized genesis DB for the node

`pwd`
```
<your directory>/nodes/0
```
`proxi init -h`

`proxi init ledger_id`

`proxi init genesis_db`

`proxi init bootstrap_account`

```text
proxi.genesis.id.yaml  proxi.yaml  proxima.yaml  proximadb  proximadb.txstore
```

`proxi db info`

```text
Command line: 'proxi db info'
Multi-state store database: proximadb
Total 1 branches in the latest slot 1
----------------- Global branch data ----------------------
  0:  ($/af7bed..) supply: 1_000_000_000_000_000, infl: 0, on chain: 999_999_999_000_000, coverage: 1_500_000_000_000_000, root: ddc71e863a21606da4055240bed7ae184cdf9479c84f70aef0c4ba9a2e3099d9

------------- Supply and inflation summary -------------
   Slots from 0 to 1 inclusive. Total 2 slots
   Number of branches: 2
   Supply: 1_000_000_000_000_000 -> 1_000_000_000_000_000 (+0, 0.000000%)
   Per sequencer along the heaviest chain:
            $/af7bedde1fea.. : last milestone in the heaviest:        [1|0br]9d171f..[0], branches: 1, balance: 1_000_000_000_000_000 -> 999_999_999_000_000 (-1_000_000)
```
`proxi db accounts`
```text
Command line: 'proxi db accounts'
Multi-state store database: proximadb
---------------- account totals at the heaviest branch ------------------
   Locked accounts: 2
      stem([0|0br]000000..[1]) :: balance: 0, outputs: 1
      addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb) :: balance: 1_000_000_000_000_000, outputs: 2
   --------------------------------
      Total in locked accounts: 1_000_000_000_000_000
   Chains: 1
      $/af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963 :: 999_999_999_000_000   seq=true branch=true
   --------------------------------
      Total on chains: 999_999_999_000_000
```

## Run node
`proxima`

