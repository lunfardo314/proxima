# General API
[get_ledger_id](#get_ledger_id)
[get_account_outputs](#get_account_outputs)
[get_chain_output](#get_chain_output)
[get_output](#get_output)
[query_txid_status](#query_txid_status)
[query_inclusion_score](#query_inclusion_score)
[submit_nowait](#submit_nowait)
[sync_info](#sync_info)
[node_info](#node_info)
[peers_info](#peers_info)
[get_latest_reliable_branch](#get_latest_reliable_branch)
[check_txid_in_lrb](#check_txid_in_lrb)


## get_ledger_id
TODO
`/api/v1/get_ledger_id`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/get_ledger_id'
```

```json
{
    "ledger_id_bytes": "001350726f78696d612074657374206c6564676572674e0d0700038d7ea4c68000c07e104fcbec1daf388ffe50a6fd3ddf006d1e24a384ff81277fee6eff8087380000000002625a0000000000004c4b400000000000027100000000002efe0700000000000000000c00000000000000011903000000003b9aca0000000000000000083219"
}
```


## get_account_outputs
TODO
`/api/v1/get_account_outputs?accountable=<EasyFL source form of the accountable lock constraint>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/get_account_outputs?accountable=a(0x033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d)'
```

```json
{
  "outputs": {
    "00001d486301153c8d8abe069e3c5e87fc34b721bb2e8886038138a014bd655200": "40020b45ab8800000000000f404c2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d",
    "80001d4869017f347dce50e146eb8f27b7edd5b493897936d2940039cddd918801": "40020b45ab8800000000000f42402345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d",
    "80003cad00014db2b8d461642ccf92177fa83f3feb4a165d8a2ed8ac8281d2db00": "40060b45ab8800038d7f7c182c6b2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7f7c182c6b1d504287626f6f742e6230840000043f84000002248800000000000000006151d7880000000000386580d103948987dc2a310986f2a9691b5b2dfcb6f634ce0d77bcb36fa4ffd0aeb1237886229954002d65b5cf4f9acee89b460896069413fdffe68994a1a36514cd01c4e8179ffa0c889079739e745425e0ed64bd810281ff"
  },
  "lrb_id": "80003cad00014db2b8d461642ccf92177fa83f3feb4a165d8a2ed8ac8281d2db"
}
```

## get_chain_output
TODO
`/api/v1/get_chain_output?chainid=<hex-encoded chain ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/get_chain_output?chainid=6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc'
```

```json
{
  "output_id": "80003cc800019ca4acabc96e3bc390c4278f8ab861c0021aa22106368bfdd4ba00",
  "output_data": "40060b45ab8800038d7f873d1bc52345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7f873d1bc51d504287626f6f742e62308400000475840000023f8800000000000000006151d7880000000000386580d103b7a35d38fa03bbb8554dcbd8abe2f42903bdc65d4a3d1f13d8bf2d86f2320397dcf8e754a9bf1e1dee3d515b312d25a70f7850d97748f47abc795538eb0a5d6e175dd6741797f18b62ec7d5c23343f59810281ff",
  "lrb_id": "80003cc800019ca4acabc96e3bc390c4278f8ab861c0021aa22106368bfdd4ba"
}
```

## get_output
TODO
`/api/v1/get_output?id=<hex-encoded output ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/get_output?id=80003d180001780694657bcff9b6a3ccd0e9146fad6e8692be33e1cf8c1d4c5a00'
```

```json
{
  "output_data": "40060b45ab8800038d7fa6b1266d2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7fa6b1266d1d504287626f6f742e62308400000515840000028f8800000000000000006151d7880000000000386580d10219960a241bda3e6475dc3c5ec4902d221f77c62cd8f90f77d212ee7ee7ebe169af56782638a96a8422e5ec87b5cd985e05aed0ef65405ff26fe7bc4a490a96eb767976a3158c29a7c81966de20076c23810281ff",
  "lrb_id": "80003d180001780694657bcff9b6a3ccd0e9146fad6e8692be33e1cf8c1d4c5a"
}
```

## query_txid_status
TODO
`/api/v1/query_txid_status?txid=<hex-encoded transaction ID>[&slots=<slot span>]`

Example:
TODO


## submit_nowait
TODO
Feedback only on parsing error, otherwise async posting
`/api/v1/submit_nowait`

Example:
TODO

## sync_info
GET sync info from the node

`/api/v1/sync_info`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/sync_info'
```

```json
{
  "synced": true,
  "current_slot": 15718,
  "lrb_slot": 15718,
  "ledger_coverage": "2_000_009_657_532_981",
  "per_sequencer": {
    "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc": {
      "synced": true,
      "latest_healthy_slot": 15718,
      "latest_committed_slot": 15718,
      "ledger_coverage": 2000009657532981
    }
  }
}
```


## node_info
GET node info from the node

`/api/v1/node_info`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/node_info'
```

```json
{
  "id": "12D3KooWSEDuiViLCgy6RvzQWeziKk79aAMikGPFnMKjnLzv9TVi",
  "version": "v0.1.3-testnet",
  "num_static_peers": 0,
  "num_dynamic_alive": 0,
  "sequencers": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc"
}
```

## peers_info
GET peers info from the node

`/api/v1/peers_info`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/peers_info'
```

```json
{
  "host_id": "12D3KooWSQWMFg78817tyNFP7GsqUvYp2TQdfXC96bw84nvJrj2Z",
  "peers": [
    {
      "id": "12D3KooWBRmgc5d2kusZ8xtXQ98iK1qtjP6CzWSxSyTeUzMJEZnV",
      "multiAddresses": [
        "/ip4/83.229.84.197/udp/4000/quic-v1"
      ],
      "is_static": false,
      "responds_to_pull": false,
      "is_alive": true,
      "when_added": 1733327100186579692,
      "last_heartbeat_received": 1733329725559708378,
      "clock_differences_quartiles": [
        9680897,
        10180110,
        10511030
      ],
      "hb_differences_quartiles": [
        2000333141,
        2000521070,
        2001048199
      ],
      "num_incoming_hb": 1312,
      "num_incoming_pull": 0,
      "num_incoming_tx": 5070
    }
  ]
}

```

## get_latest_reliable_branch
GET latest reliable branch
`/api/v1/get_latest_reliable_branch`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/api/v1/get_latest_reliable_branch'
```

```json
{
  "root_record": {
    "root": "13511d56313d105804a18bb78e7d008bf367fc90c96f20fe84003cad5c41da2b",
    "sequencer_id": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc",
    "ledger_coverage": 2000010399831606,
    "slot_inflation": 6557224,
    "supply": 1000005215558988
  },
  "branch_id": "80003da0000109a144ad17b0fb53f5378cecad81c93aeb87d1081dd02c6eff18"
}
```

## check_txid_in_lrb
GET latest reliable branch and check if transaction ID is in it
`/api/v1/check_txid_in_lrb?txid=<hex-encoded transaction ID>`

Example:
TODO

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

Example:
TODO

## decompile_bytecode
Decompiles bytecode in the context of the ledger of the node to EasyFL script

`/txapi/v1/decompile_bytecode?bytecode=<hex-encoded bytecode>`

Example:
TODO

## parse_output_data
By given raw data of the output, parses it as lazyarray
and decompiles each of constraint scripts. Returns list of decompiled constraint scripts
Essential difference with the parse-output is that it does not need to assume particular LRB

`/txapi/v1/parse_output_data?output_data=<hex-encoded output binary>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/txapi/v1/parse_output_data?output_data=40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff'
```

```json
{
  "data": "40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff",
  "constraints": [
    "amount(u64/1000005667366158)",
    "a(0x033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d)",
    "chain(0x6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc000200)",
    "sequencer(2, u64/1000005667366158)",
    "or(0x626f6f742e6230,0x000006ad,0x0000035c,0x0000000000000000)",
    "inflation(0x0000000000386580, 0x022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35, 2, 0xff)"
  ],
  "amount": 1000005667366158,
  "chain_id": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc"
}
```

## parse_output
By given output ID, finds raw output data on LRB state, parses the it as lazyarray
and decompiles each of constraint scripts. Returns list of decompiled constraint scripts

`/txapi/v1/parse_output?output_id=<hex-encoded output ID>`

Example:

``` bash
curl -L -X GET 'http://localhost:8000/txapi/v1/parse_output?output_id=80003e0a00015dc6362a7e6ed5a422b2edc5e46d262d690cfdea81dd997243fd00'
```

```json
{
  "data": "40060b45ab8800038d7ff693a50e2345b3a0033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d2645c2a36393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc0002000d49d181028800038d7ff693a50e1d504287626f6f742e623084000006ad840000035c8800000000000000006151d7880000000000386580d1022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35810281ff",
  "constraints": [
    "amount(u64/1000005667366158)",
    "a(0x033d48aa6f02b3f37811ae82d9c383855d3d23373cbd28ab94639fdd94a4f02d)",
    "chain(0x6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc000200)",
    "sequencer(2, u64/1000005667366158)",
    "or(0x626f6f742e6230,0x000006ad,0x0000035c,0x0000000000000000)",
    "inflation(0x0000000000386580, 0x022cee903827166af9c0257be156222ae34c9279831a294fb5213647a3bcbe7a3e203d100f3ec2095fb076c65ed1d29a680c05c7993d43e7fdd0a779e8f783943a50b0a1ba3b373830cc2f447a030edb35, 2, 0xff)"
  ],
  "amount": 1000005667366158,
  "chain_id": "6393b6781206a652070e78d1391bc467e9d9704e9aa59ec7f7131f329d662dcc"
}
```

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



