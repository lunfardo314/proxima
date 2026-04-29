## Running node with the sequencer

**Sequencer** is a program run as a part of a node. It is building **sequencer chain** by consolidating chains of
other sequencers and by consuming tag-along outputs sent to it. 

Sequencers are selfish and a seeking profit in the form of inflation. 
This selfish behavior brings profit only when sequencers cooperate. 
The emerging behavior results in decentralized consensus on the ledger. **Sequencers make consensus in Proxima**.

Other users send tag-along fees to sequencers in their transactions and sequencers pull those transactions into the decentralized ledger. 

The core of the node supports multi-sequencer mode; however, in the current version we are limiting the 
number of sequencers in the node to one.

- if a node runs a sequencer, it is a _sequencer node_ 
- if a node does not run a sequencer, it is an _access node_ 

To run a sequencer in the testnet, one needs at least 1.000.000.000 tokens (1 millionth of the initial supply). 

### Steps to run the sequencer:

1. make your access node running and synced. See instructions in [Running access node](run_access.md)
2. create a new chain origin with `proxi node mkchain <amount>`. Make sure you don't use the whole amount balance for the chain.
It is recommended to have at least `100.000.000` tokens for tag-along fees, spamming and other purposes.
3. once you created chain origin, you can check it with `proxi node allchains`
4. configure `sequencer` section in the node configuration profile `proxima.yaml` of your access node the following way:

```yaml
sequencer:
  enable: true
  name: <sequencer name>
  chain_id: <chain ID>
  controller_key_file: proxima_sequencer.key
  pace: 5
```

With `sequencer.enable` = `true/false` you can enable or disable the start of the sequencer at the startup of the node. With `enable: false`
node is just an access node.

`sequencer.name` is any mnemonic name used for the sequencer. It will appear in the logs and in the sequencer transactions.
It is recommended to have it no longer than 4-6 characters.

`sequencer.chain_id` is the ID of the newly created chain (hex encoded, not with `$/` prefix). It is also called _sequencer ID_.

`sequencer.controller_key_file` is the path to a file containing the controlling private key (hex encoded).
The file should have restricted permissions (`chmod 0600`). The `proxi node setup_seq` command creates this file automatically as `proxima_sequencer.key`.

Alternatively, `sequencer.controller_key` can be used to specify the private key inline in the YAML (less secure, supported for backward compatibility).

The controller key can also be provided via the `PROXIMA_SEQUENCER_KEY` environment variable (hex-encoded).
This is useful for containerized deployments and CI/CD pipelines where secrets are injected as environment variables.

Priority: `PROXIMA_SEQUENCER_KEY` env var > `controller_key_file` > `controller_key` (inline).

### Encrypting the key file with a passphrase

For additional security, the plaintext key file can be encrypted with a passphrase:

```bash
proxi util encrypt_key
```

This reads `proxima_sequencer.key`, prompts for a passphrase, and creates `proxima_sequencer.keystore` — a JSON file
with the key encrypted using AES-256-GCM (Argon2id key derivation). It also updates `proxima.yaml` to point to the keystore.

The keystore format is detected automatically. When starting the node, the passphrase is read from the `PROXIMA_KEY_PASSPHRASE`
environment variable. If not set, the node prompts on stdin.

To verify a keystore file:

```bash
proxi util check_keystore
```

This decrypts the keystore and verifies the key matches the stored public key.

`sequencer.pace` parameter is minimum number of ticks between two subsequent sequencers transactions. In the testnet version 
it should not be less than `3` and not exceed `20` or so. 1 tick is 80 milliseconds on the clock-time scale.

5. start the node as usually. Node will log details of the sequencer. It will take 10 to 15 seconds until sequencer starts
issuing sequencer transactions and earning inflation with branch inflation bonus (when lucky).

6. adjust your wallet profile `proxi.yaml` by putting your _sequencer ID_ as own (controlled) sequencer in `wallet.sequencer_id`. 
With this configured properly, you will be able to withdraw part of your funds from the running sequencer chain 
without stopping the sequencer with command `proxi node seq withdraw <amount> [-t <target address>]`.
Note that every transaction costs fees. So, it is smart to configure your wallet's tag-along sequencer to your own sequencer.
This way all the fees will go to yourself: making your transactions essentially fee-less. 

### Useful 
Configuration key `logger.verbosity` specifies logging level for the sequencer transaction:

`logger.verbosity: 0` only branch transactions are displayed in the log

`logger.verbosity: 1` branch and other sequencer transactions are displayed in the log

