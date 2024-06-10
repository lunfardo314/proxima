## CLI wallet program `proxi`

`proxi` is a small CLI program with basic tools for Proxima.
The program can be compiled by typing `go install` in the `<root>/proxi` directory of the Proxima source.

Commands `proxi -h`, `proxi db -h`, `proxi node -h`, etc. will display help text. 

Some commands, for example `proxi gen` are completely stand alone and does not require any config profile.
Most of the commands require configuration profile in the working directory. 

`proxi` configuration profile usually is file `proxi.yaml`. If we use a file 
with another name, say `proxi2.yaml`, we have to specify it explicitly in the command line with flag `-c` 
and profile file mame without extension, for example `proxi node balance -c proxi2`.

`proxi` commands has a form `proxi <cmd group> <other args and flags>`, where `<cmd group>` is one of the following:

* `proxi init` for admin commands, for example the ones needed to start a node 
* `proxi db`  for commands which access multi-state database directly. They all will fail if node is running
* `proxi node` for commands which access node via API. They all require configuration profile and a running node
* `proxi gen` generating privare keys (only for testing!)

### 1. Create configuration profile and the wallet

The command `proxi init wallet` generates private key from the provided seed and system randomness.
It also creates configuration profile `proxi.yaml`.

The file will contain something like this (with comments):

```yaml
private_key: af274b0363f484f8d113a9e17831ff3acd285fd152c5179db42ab0ff976e23153a51eabb1c19f1b5e784d086a6bf176c8ada3c248f25da93f7362c35eb1fc660
account: addressED25519(0x7450c426206c4164bc84ff30a14bdf72603b563e26a1d43973bc67cdb59033d8)
sequencer_id: <own sequencer ID>
api:
  endpoint: http://127.0.0.1:8000
tag_along:
  sequencer_id: af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963
  fee: 500
finality:
  inclusion_threshold:
    numerator: 2
    denominator: 3
  weak: false
spammer:
  bundle_size: 5
  max_duration_minutes: 0
  max_transactions: 0
  output_amount: 1000
  pace: 10
  scenario: standard
  submit_nowait: false
  tag_along:
    fee: 500
  target: <target address in EasyFL format>
  trace_on_node: false
```

Usually some adjustments are needed to complete the profile. 

`wallet.private_key` contains hex encoded raw data of the ED25519 private key. The file must be kept secret 
because of this private key. 

`wallet.account` contains address constraint in the _EasyFL_ format which matches the private key. 

`sequencer_id` is an optional field if you do not run sequencer. It contains `sequencer ID` of the sequencer controlled by this wallet. 
It is necessary in order to access sequencer controlled by this private key with the `proxi node seq withdraw ..` command. 

`api.endpoint` must contain endpoint for the node's API

`tag_along.sequencer_id` is a mandatory field for any commands which issue transactions, such as `proxi node transfer`.
It must contain sequencer which is used as tag-along sequencer. Each issued transaction will contain so-called _tag-along output_
which simply sends amount of tokens specified in `tag_along.fee` to the sequencer in `tag_along.sequencer_id`. 
The sequencer will consume the fee input and this will pull the transaction into the next ledger state. 
By default, `proxi.yaml` is initialized with the static constant of 
bootstrap sequencer ID `af7bedde1fea222230b82d63d5b665ac75afbe4ad3f75999bb3386cf994a6963`. 

Other default values can be left as is in the beginning.

### 2. Run spammer from the wallet
Spammer is run with the command `proxi node spam`. 
It periodically sends tokens to the target address in bundles of transactions. It waits each bundle of transactions 
reaches finality before sending the next one.

The bundle of transactions is a chain of transactions where only the last one (tip) contains tag-along output.

Configure tag-along sequencer consumes the output and this way pull the whole bundle into the ledger state.  