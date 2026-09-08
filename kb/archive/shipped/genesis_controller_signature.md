> SHIPPED

# Genesis controller signature as a ledger constant

Shipped 2026-09-08 in `bf490679` (develop and master). Hardfork: the library
hash of any new genesis changes.

## What changed

The ledger constant `constGenesisControllerPublicKey` became
`constGenesisControllerSignature`. It holds the genesis controller's signature
data in the format every transaction signature uses: one signature type byte,
the ED25519 signature, the public key. 97 bytes in total.

The signed message is `concat(constDescription, constGenesisTimeUnix)`: the
description bytes followed by the genesis Unix time as the 8-byte big-endian
u64 the constant itself is encoded as. Wallet side the same message is built
by `txbuildercore.GenesisControllerSignedMessage`.

The constant stays `immutable: true`, so easyfl refuses to replace it in an
upgrade, and `validateGenesisIdentityImmutability` already pins the
description and genesis time. Together that pins the claim to genesis.

## Why

A bare public key in the constants proved nothing. Anyone could put anyone's
key into a ledger definitions file, and the key holder could not show that
they, and not an impostor, produced the library with that hash. With the
signature the controller's key claims the specific description and genesis
time, and through them the library hash they are compiled into. The
public key is still available: it rides inside the signature data.

## Where it is enforced

- `ledger.ConstantsFromLibrary` asserts the signature verifies. A library with
  a bad or forged constant fails to load.
- `txbuildercore.Constants.UnmarshalJSON` verifies it when a wallet reads
  `/api/v1/ledger_constants`. The API field is `genesis_controller_signature`.
- In EasyFL, `validSignature(concat(constDescription, constGenesisTimeUnix),
  constGenesisControllerSignature)` is true, and
  `txHolderID(constGenesisControllerSignature)` is the genesis controller's
  address. Nothing on the transaction path evaluates this; it is for anyone
  checking a definitions file.

## Shape of the code

`InitParameters` carries `GenesisControllerPrivateKey` instead of the public
key, and `ConstantsJSONFromParamsUpgrade0` signs when it renders the JSON, so a
description set after `DefaultParameters` is still what gets signed. Readers
take the key through `Constants.GenesisControllerPublicKey()`.
`base.SignatureDataED25519` builds the wire format and is shared with
`TxBuilder.SignED25519`.

## Rejected

- Signing only the description, or only the genesis time. Either alone is not
  enough to identify one ledger; both together are what `LedgerIdentity`
  stores at the trie root.
- A 4-byte genesis time in the message, matching `LedgerIdentity.Bytes()`.
  Rejected so the message equals the EasyFL `concat` of the two constants and
  can be checked without Go.
- Verifying in an EasyFL constraint on the transaction path. Nothing there
  needs it and it would cost a signature check per transaction.

## Not changed

`tests/node-docker-setup/node/proxima.genesis.id.yaml` still carries a
`genesis_controller_public_key` line. It is a legacy fixture nothing reads.
