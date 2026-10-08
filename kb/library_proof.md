# Library commitment proof — the wallet verifies the library it compiles with

> **LIVE — approved and built 2026-10-08** on `develop-take1`: verifier and
> wire types in `ledger/txbuildercore/library_proof.go`, the proof in
> `multistate.Readable.UTXOProof`, the `commitment` object in the
> `get_ledger_definition` response, `client.GetLibrary` and the wasm
> `InitLibrary` refusing a library that does not verify, tests in
> `ledger/multistate/library_proof_test.go` and
> `api/server/library_commitment_test.go`. Closes the one gap the input
> commitment leaves open on the wallet-to-node path: the library the wallet
> compiles its produced outputs with is fetched from the node and nothing binds
> it to the ledger. The node returns, with the library, everything needed to
> prove that the baseline state of a branch commits to that library, and the
> wallet verifies it with no trust in the node. The only trust input left is the
> branch ID; anchoring that across several nodes is a separate function,
> `kb/api_witnesses.md`. No ledger change, no hardfork.

## 1. Problem

The wallet (`proxi`, the wasm wallet) builds transactions with a library it
fetches over plain HTTP: `client.GetLibrary` calls `get_ledger_definition`,
parses the JSON and hands it to `txbuildercore.NewLibrary`. The input
commitment makes the consumed side safe against anyone on the path: a wallet
fed false UTXOs builds a transaction that commits to outputs which do not exist,
and every honest node rejects it. A false balance is a display lie, a tampered
submission fails the signature, so the attacker gets denial and never loss.

The produced side is different. The wallet compiles the outputs it creates by
name, `addressED25519(0x…)`, and the library JSON is what maps the name to a
function code. A tampered library remaps names to codes; honest nodes interpret
the resulting bytecode with their own library. To profit, the attacker needs a
real constraint which, given the arguments the wallet supplies, yields a lock
the attacker can spend. The arguments are 32-byte hashes and amounts the user
does not choose and the lock table is short, so no exploit is known today. It is
a gap in the reasoning, not an attack; for a network that may become mainnet
the gap is closed rather than argued away.

Pinning the library hash in the wallet profile was rejected: the hash changes
with every upgrade and the profile would have to be edited by hand each time.
The ledger already commits to the library; the wallet only needs the proof.

## 2. What is already committed

Three facts, all on `develop` today and verified against the code.

**The library hash is in the state.** Every library upgrade is a synthetic,
unspendable UTXO in the ledger-state trie, injected by the first branch at or
after the upgrade slot (`multistate.InjectMissingUpgradeUTXOs`). Its ID is
`base.UpgradeOutputID(upgradeSlot)`, deterministic, and its state key is the
ledger-state partition byte followed by the output ID. The value under that
key is the output bytes. Output elements 3, 4 and 5 are inline data: the
library hash, the previous library hash, the previous upgrade slot
(`ledger.UpgradeUTXO`, `ledger.ParseUpgradeUTXO`). The UTXOs form a hash chain
back to the base EasyFL library, and since nothing consumes them, the baseline
state of any branch holds every upgrade so far.

**The branch commits to a state root.** The stem output of a branch has an
unconstrained inline-data tuple at element 3 (`ledger.OracleData`); its fifth
element is `BaselineRoot`, the trie root of the branch's predecessor, the
baseline the branch was built on. The attacher checks it against the local root
of that baseline (`core/attacher/check.go`), so on the honest network every
branch binds the root of the state one branch back.

**The branch ID binds the bytes.** A transaction ID is the timestamp, the
sequencer flag and the hash of the transaction tree
(`txbuildercore.TxIDFromTree`). Branch bytes cannot be swapped under a given
ID.

Unitrie provides both halves of the Merkle proof: `ProofImmutable(key, trie)`
on the `trie_blake2b` commitment model produces it, and the standalone package
`trie_blake2b_verify` validates it against a root and a terminal value with no
dependency on the node.

**The wallet computes the hash itself.** The engine hashes the compiled
essence only: function codes, names, argument counts, bytecodes, embedded-as
keys, immutable flags and version data. Function bodies are not hashed, so a
library built wallet-side without embedded Go bodies serialises identically.
Verified 2026-10-08: the node library, the wallet library built from the node's
JSON and the `hash` field inside the JSON agree. The `hash` field is never used
by the proof: the attacker writes the JSON.

## 3. The chain of custody

```
branch ID  ──(txid = f(tree))──►  branch tx bytes
           ──(stem output, element 3, tuple element 4)──►  baseline root
           ──(Merkle proof, key = partition ‖ UpgradeOutputID(slot))──►  upgrade UTXO bytes
           ──(output element 3)──►  library hash
           ──(LibraryHash() of the library built from the JSON)──►  library JSON
```

Every arrow is a hash or a byte parse the wallet performs itself. The chain has
exactly one input the wallet cannot derive: the branch ID. That is the job of
`kb/api_witnesses.md`; this document treats the branch ID as given.

One branch proves every upgrade: the upgrade UTXOs are never consumed, so the
baseline state of the latest reliable branch holds all of them. The proof for
an older library (the `slot` parameter) uses the same branch.

## 4. The API

`get_ledger_definition` is extended, not replaced. The response
(`api.LedgerDefinition`) gains one object:

```json
"commitment": {
  "branch_id":          "<hex, the transaction ID of the branch>",
  "branch_tx_bytes":    "<hex, raw transaction bytes of that branch>",
  "upgrade_utxo_bytes": "<hex, output bytes of the upgrade UTXO for upgrade_slot>",
  "proof":              "<hex, trie_blake2b.MerkleProof.Bytes()>",
  "error":              "<set instead of the four fields above when the node cannot prove>"
}
```

The server (`api/server`) builds it from what it already has:

1. `GetLatestReliableBranch()` gives the branch; its `Stem.Output.OracleData()`
   gives `BaselineRoot`.
2. `common.VectorCommitmentFromBytes(ledger.CommitmentModel, root)` and
   `multistate.NewReadable(StateStore(), root)` open the baseline state.
3. A new `Readable.Proof(key)` wraps `ledger.CommitmentModel.ProofImmutable`
   over the reader's trie. The key is the ledger-state partition byte followed
   by `base.UpgradeOutputID(upgradeSlot)`. The value is read with
   `Readable.GetUTXO`.
4. `TxBytesStore().GetTxBytes(branchID)` gives the branch bytes. The store is
   append-only, so a committed branch is always there.

If the upgrade UTXO is absent from the baseline (the latest reliable branch is
the one that injected it, so the UTXO is in its own state and not yet in a
baseline) the node sets `error` and the wallet refuses the library; a slot
later it is there. This window exists once per upgrade and nowhere else.

Fetching the library and fetching its proof are one request on purpose: the
wallet never holds a library it has not verified, and a node that cannot prove
is indistinguishable from one that lies.

Size: a branch transaction is a few KB, the proof under an arity-16 trie for
a key this shallow is a few KB. Once per process.

## 5. The verifier

One pure function, wallet side, shared by `proxi` and the wasm wallet:

```go
// ledger/txbuildercore/library_proof.go
func VerifyLibraryCommitment(c *LibraryCommitment, upgradeSlot uint32, lib *Library[any]) error
```

`LibraryCommitment` is the decoded `commitment` object. The function fails on
the first step that does not hold, with the step named in the error:

1. Parse `branch_tx_bytes` as a tree (`tuples.TreeFromBytesReadOnly`),
   compute `TxIDFromTree`, require it equal to `branch_id`, require the ID to
   be a sequencer transaction at tick 0 (a branch).
2. Read the stem output index: byte 1 of the element at
   `PathToSequencerDataBytes`. Read that produced output under
   `PathToProducedOutputs`, parse it with `txbuildercore.OutputFromBytes`,
   take element 3, strip the inline-data prefix, parse the tuple, take element
   4: the baseline root, `TrieHashSize` bytes.
3. Parse `proof` with `trie_blake2b.ProofFromBytes`. Validate it against the
   root together with the terminal, `ValidateWithTerminal(proof, root,
   upgrade_utxo_bytes)`. Require the proof's key to equal the expected key,
   partition byte and `base.UpgradeOutputID(upgradeSlot)`, unpacked with the
   proof's arity (`common.UnpackBytes`). A proof of absence fails here.
4. Parse `upgrade_utxo_bytes` with `txbuildercore.OutputFromBytes`; elements
   3, 4, 5 are the library hash, the previous hash, the previous slot. Require
   the previous hash and slot to equal the `prev_library_hash` and
   `prev_upgrade_slot` of the same response.
5. Require `lib.LibraryHash()` to equal the library hash from the UTXO.

Two byte-level parsers move down so the wallet can use them without importing
`ledger`: the upgrade UTXO element parse becomes `txbuildercore.UpgradeUTXOView`
and the stem tuple's baseline root becomes `txbuildercore.StemBaselineRoot`;
`ledger.ParseUpgradeUTXO` and `ledger.OracleDataFromBytes` keep their typed
API over them. The ledger-state partition byte and the state's commitment
model (hexary trie, 24-byte blake2b) become `txbuildercore` definitions that
`multistate.TriePartitionLedgerState` and `ledger.CommitmentModel` refer to,
so the key and the model are spelled in one place. The model is needed
wallet-side because the verifier compares the proof against the terminal
commitment of the value, not the value itself. `txbuildercore` gains the
`trie_blake2b` imports; the wasm binary grows by those packages.

## 6. Wiring

- `client.GetLibrary` decodes the commitment, builds the library, runs the
  verifier and returns the error. A response with no `commitment` object (a
  node older than this spec) fails with a message saying so. Every take 1 node
  provides it; there is no flag to skip the check.
- `glb.GetTxLibrary` is unchanged in shape: the verification happens inside
  the one fetch it already does, once per process, and a failure aborts the
  command as any other fetch failure does. After the proof it hands the
  `branch_id` to the witness check of `kb/api_witnesses.md`.
- The wasm wallet's `InitLibrary` takes the commitment alongside the JSON and
  verifies before accepting the library. Its `LibraryHash` export returns the
  computed hash, not the JSON's `hash` field it echoes today; that field is
  attacker-controlled and the export is misleading as it stands.
- `InitLedgerFromNode`, the singleton loader kept for the chess, inflation and
  snapshot commands, walks the whole upgrade chain and verifies every library
  it loads against the same branch: one branch proves all of them (§3).

## 7. Out of scope

- **The branch ID.** Treated as given; anchored by `kb/api_witnesses.md`.
- **Ledger constants.** `get_ledger_constants` is fetched separately and is
  not covered. Tampered constants make the wallet build transactions the
  network rejects, never ones that lose funds, so denial only. Deriving them
  wallet-side from the verified library is the clean closure and a separate
  step.
- **Transport security.** This proof is why take 1 ships plain HTTP: it
  removes the integrity argument for TLS on the API path. The residual case,
  an attacker on every path the user has, is what the witness list reduces and
  only TLS removes.

## 8. Tests

- `ledger/multistate`: build a state with its genesis upgrade UTXO, commit one
  branch, produce the proof against that branch's baseline root and run
  `VerifyLibraryCommitment` on the branch's bytes. Then the negatives: a
  library with one function code remapped fails at step 5, a flipped root byte
  fails at step 3, a proof for a different key fails at step 3, bytes of a
  different branch fail at step 1.
- `api/client` against a running test node (`tests/`): `GetLibrary` succeeds
  on an honest node and fails when the handler's response is edited in
  flight.
