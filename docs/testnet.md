## Participating in the open testnet

Proxima testnet is an experimental network, intended for testing node software and various aspects of the Proxima concept.

We have been running several of them, each with at least 9 nodes and 5 sequencers among them. Normally we aim to control the testnet
by owning a majority of token supply. This is due to the experimental nature of the networks and frequent breaking changes. 
After each breaking change, we have to reset the ledger state from genesis.

Testnet versions has form `v0.x.y-testnet`, where `x` is breaking change (incompatible with previous) and `y` is non-breaking upgrade. 

Starting from version `v0.4.0-testnet` Proxima node implements all main functions of the core protocol. 
Subsequent versions are upgrades and improvements.

Version `v0.6.0-testnet` implements scalable delegation function and state bloat prevention measures (storage deposit).

### Other docs
Please read at least basic docs on [proxi](proxi.md), [delegation](delegate.md) and other available materials. 

### Public access points
These are public API endpoints to access from `proxi` or for other purposes:

* http://113.30.191.219:8001
* http://63.250.56.190:8001
* http://83.229.84.197:8001
* http://5.180.181.103:8001

The faucet is available on `113.30.191.219:9500`. You need the following section in your `proxi.yaml`:

```yaml
faucet:
    port: 9500
    addr: 113.30.191.219
```

### How to get tokens?
Use command `proxi node getfunds` to get tokens to your wallet as defined by the `proxi.yaml` in you current directory.
You can do it once per day. Faucet will send `1.000.000.000.000` tokens to your account. 

Check you balance with command `proxi node balance`. If everything is ok, the requested tokens will come after 15-30 seconds. 

### What can you do with your tokens?

#### Transfer tokens between accounts

To send tokens between accounts, you use command `proxi node transfer`. See [proxi docs](proxi.md). 
Note, that for this `proxi.yaml` must be configured properly. In particular, _tag-along sequencer_ and _tag-along fees_ must be
configured properly. You can list all sequencers with command `proxi node allchains -q` and choose one of them as tag-along. 

#### Earn inflation by delegation
Please read [delegation](delegate.md). It is **strongly encouraged** to delegate all but some minimum amount (say `100.000.000`) of your tokens, 
immediately you receive them with `proxi node getfunds`. 

All sequencers with delegation information can be listed with `proxi node allchains -q`. It is easy to choose one of them for delegation
and tag-along.

Your delegated tokens will contribute to the security of the network and, in exchange, will earn you inflation around **9-10% annually**. 
If your tokens remain passive in your normal account (which has address in the form `a(0x<hex>)`), you will not receive any inflation. 

#### Earn inflation by running sequencer
To run a sequencer, you need two things:
1. run an _access node_. The *access node* is a "normal" node which permanently keeps valid ledger on it. Access node do not run a sequencer. See [Running access node](run_access.md) for detailed instructions. Note that it is pretty easy to run access node. It does not require owning any tokens. It does not contribute to the security of the network, just provides secure access to it. However, it contributes to the decentralization of the network by providing replicas of the valid ledger. It is possible to recover the whole network from one node (you will need private keys controlling token accounts of course)   
2. configure and run a **sequencer** on that access node (then we call it _sequencer node_). See [Running node with the sequencer](run_sequencer.md). To run a sequencer you will need tokens.  

Sequencers are programs that generate inflation and therefore contribute to the security of the network on behalf of the token holder. 
In addition to the usual inflation, sequencers may win the lottery for the *branch inflation bonus*.

### Disclaimer

We will do our best to help you on the Proxima Discord channel `#testnet`. Please note, however, our resources are very limited. 
We count on growing community which can help each other.

Please also note that:

* tokens are not real, they have 0 value. Only for testing. T
* the Proxima software at this stage is experimental and definitely contains bugs. Do not use it in production! 

