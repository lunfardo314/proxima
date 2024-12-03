
# TX API

[compile_script](#compile_script)
[decompile_bytecode](#decompile_bytecode)
[parse_output_data](#parse_output_data)
[parse_output](#parse_output)
[get_txbytes](#get_txbytes)
[get_parsed_transaction](#get_parsed_transaction)
[get_vertex_dep](#get_vertex_dep)


## compile_script
Compiles EasyFL script in the context of the ledger of the node and returns bytecode

`/txapi/v1/compile_script?source=<script source in EasyFL>`


## decompile_bytecode
Decompiles bytecode in the context of the ledger of the node to EasyFL script

`/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>`

## parse_output_data
By given raw data of the output, parses it as lazyarray
and decompiles each of constraint scripts. Returns list of decompiled constraint scripts
Essential difference with the parse-output is that it does not need to assume particular LRB

`/txapi/v1/parse_output_data?output_data=<hex-encoded output binary>`

## parse_output
By given output ID, finds raw output data on LRB state, parses the it as lazyarray
and decompiles each of constraint scripts. Returns list of decompiled constraint scripts

`/txapi/v1/parse_output?output_id=<hex-encoded output ID>`

## get_txbytes
By given transaction ID, returns raw transaction bytes (canonical form of tx) and metadata (if it exists)

`/txapi/v1/get_txbytes?txid=<hex-encoded transaction ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/txapi/v1/get_txbytes?txid=8000001400017a49603b8371e0063c4c0fb8fb7d91dd9968f308907d9ff2f8b7
```

```json
{
  "tx_bytes": "800a000f40020940030001ff03000200020000004640022180000013190028be2c49083ae99f41d5fd3950e35bea475f1344562e898adf9d00218000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c001011e4002e740060b45ab8800038d7eab3cc7952345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7eab3cc7951d504287626f6f742e6230840000002284000000128800000000000000006151d7880000000000386580d102f8ad2db2c9748968c1612ee12cf91688e084527b221b03a3a1e5d3cc23f5ee5db5f2493c06d03f9161ad4afb18379877083de5c80a0b8b3e5a194f570c7a65502c04cc806621857e046b7d64b433c733810281ff3340020b45ab8800000000000000002445c6a18000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c00100606cc2dab71a8128ec5feb992af3247027acc05c360d62fc794b638b901828f586cb0e561ed07fbff49334d8bbe64b5484c075b5d4013423c408db37b77cbece09c07e104fcbec1daf388ffe50a6fd3ddf006d1e24a384ff81277fee6eff8087380002000100050000001400000800038d7eab3cc79500204aa9852da4fabc579d5516bb850bcceaa1531ac7cf44853614aef51bb6b3ba0d0002000000020000",
  "tx_metadata": {
    "ledger_coverage": 1999987795299295,
    "slot_inflation": 6055219,
    "supply": 1000000109414869
  }
}
```


## get_parsed_transaction
By the given transaction ID, returns parsed transaction in JSON form. The JSON form contains all elements
of the transaction except signature, but it is not a canonical form. Primary purpose of JSON form of the transaction
is to use it in frontends, like explorers and visualizers.

`/txapi/v1/get_parsed_transaction?txid=<hex-encoded transaction ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/txapi/v1/get_parsed_transaction?txid=8000001400017a49603b8371e0063c4c0fb8fb7d91dd9968f308907d9ff2f8b7'
```

```json
{
  "id": "8000001400017a49603b8371e0063c4c0fb8fb7d91dd9968f308907d9ff2f8b7",
  "total_amount": 1000000108414869,
  "total_inflation": 1959219,
  "is_branch": true,
  "sequencer_tx_data": {
    "sequencer_id": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc",
    "sequencer_output_index": 0,
    "stem_output_index": 1,
    "milestone_data": {
      "name": "boot.b0",
      "minimum_fee": 0,
      "chain_height": 34,
      "branch_height": 18
    }
  },
  "sender": "a(0x033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d)",
  "signature": "6cc2dab71a8128ec5feb992af3247027acc05c360d62fc794b638b901828f586cb0e561ed07fbff49334d8bbe64b5484c075b5d4013423c408db37b77cbece09c07e104fcbec1daf388ffe50a6fd3ddf006d1e24a384ff81277fee6eff808738",
  "inputs": [
    {
      "output_id": "80000013190028be2c49083ae99f41d5fd3950e35bea475f1344562e898adf9d00",
      "unlock_data": "40030001ff03000200"
    },
    {
      "output_id": "8000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c001",
      "unlock_data": "0000"
    }
  ],
  "outputs": [
    {
      "data": "40060b45ab8800038d7eab3cc7952345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7eab3cc7951d504287626f6f742e6230840000002284000000128800000000000000006151d7880000000000386580d102f8ad2db2c9748968c1612ee12cf91688e084527b221b03a3a1e5d3cc23f5ee5db5f2493c06d03f9161ad4afb18379877083de5c80a0b8b3e5a194f570c7a65502c04cc806621857e046b7d64b433c733810281ff",
      "constraints": [
        "amount(u64/1000000108414869)",
        "a(0x033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d)",
        "chain(0x6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc000200)",
        "sequencer(2, u64/1000000108414869)",
        "or(0x626f6f742e6230,0x00000022,0x00000012,0x0000000000000000)",
        "inflation(0x0000000000386580, 0x02f8ad2db2c9748968c1612ee12cf91688e084527b221b03a3a1e5d3cc23f5ee5db5f2493c06d03f9161ad4afb18379877083de5c80a0b8b3e5a194f570c7a65502c04cc806621857e046b7d64b433c733, 2, 0xff)"
      ],
      "amount": 1000000108414869,
      "chain_id": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc"
    },
    {
      "data": "40020b45ab8800000000000000002445c6a18000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c001",
      "constraints": [
        "amount(u64/0)",
        "stemLock(0x8000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c001)"
      ],
      "amount": 0
    }
  ],
  "tx_metadata": {
    "ledger_coverage": 1999987795299295,
    "slot_inflation": 6055219,
    "supply": 1000000109414869
  }
}
```

## get_vertex_dep
By the given transaction ID, returns compressed for of the DAG vertex. Its primary use is DAG visualizers

`/txapi/v1/get_vertex_dep?txid=<hex-encoded transaction ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/txapi/v1/get_vertex_dep?txid=8000001400017a49603b8371e0063c4c0fb8fb7d91dd9968f308907d9ff2f8b7'
```

```json
{
  "id": "8000001400017a49603b8371e0063c4c0fb8fb7d91dd9968f308907d9ff2f8b7",
  "total_amount": 1000000108414869,
  "total_inflation": 1959219,
  "is_sequencer_tx": true,
  "is_branch": true,
  "sequencer_input_index": 0,
  "stem_input_index": 1,
  "input_dependencies": [
    "80000013190028be2c49083ae99f41d5fd3950e35bea475f1344562e898adf9d00",
    "8000001300017eaa649b97b3adfbd6ab054aad50b8705ddd7eb7843cf17631c001"
  ]
}
```



