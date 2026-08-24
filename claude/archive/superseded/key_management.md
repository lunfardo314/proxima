# Key management

## Goal 
Make key management uniform across Proxima codebase.

## Requirements

### Keystore

* `keystore` package is the basis
* same `json`-based key file format should be used for both encrypted and unencrypted keys
* for keys the extended current `keystore` file format should be used as basis:
   * if key is encrypted, it should be recognized as such. It can contain optional field `hint` used as part of the prompt to provide passkey
   * if key is not encrypted, the file only contains relevant fields including privet key itself
* `publicKey` and `senderID` fields must be present in both encrypted and unencrypted cases
* default file for the key file is `proxima.key`

### Config
* only `.key` files are used in both sequencer config profile and `proxi` wallet config profile
* remove possibility to use plain private keys in `proxima.yaml` and `proxi.yaml` config files, only references to `.key` are allowed
* in case `.key` file is encrypted, `proxima` and `proxi` should ask for passphrase
* the above means, node with the sequencer that uses encrypted key cannot be run by `systemd` because there's no `stdin` 

### Utilities
* All key/keystore management utilities must be implemented as `proxi util key ..` subcommands
* A Proxi command must be able to encrypt/decrypt keystore consistently modifying existing keys store or making another 
* Current commands `proxi init node` and `proxi init wallet` may ask if to use exiting key from the file `.key` or generate a new one.
In the latter case private key must be generated using utility functions available in `proxi` codebase and `.key` must be created. 
User may opt in key encryption right after generation