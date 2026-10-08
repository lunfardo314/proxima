# Witness endpoints — anchoring the branch ID across several nodes

> **LIVE — approved and built 2026-10-08**: `glb.WitnessURLs`
> and `glb.VerifyBranchWithWitnesses` in `proxi/glb/witness.go`, run once per
> process from `glb.GetTxLibrary` and `glb.InitLedgerFromNode` on the branch of
> the library proof; `proxi config wallet` renders `api.node_urls` with the three
> public nodes; tests in `proxi/glb/witness_test.go`. The library proof of
> `kb/library_proof.md` reduces what the wallet must trust to one 32-byte
> value, the branch ID the node names. This document is the function that
> checks that value against other nodes, and the wallet-profile key that lists
> them. Wallet side only, `proxi`; no node change.

## 1. Problem

Every arrow in the library proof is a hash the wallet recomputes. The first
link is not: the wallet gets the branch ID from the node it is talking to, over
the same connection it gets everything else. An attacker on that path forges
branch bytes with a root of their own, builds a trie around a forged upgrade
UTXO and hands the wallet a proof that verifies. The wallet cannot validate the
branch transaction itself, because validating needs the very library in
question.

What the wallet can do is ask other nodes whether that branch is real. A branch
committed by independent nodes and on their reliable lineage is one the honest
network produced, and the attacker would have to sit on every path the user has
to forge agreement.

## 2. Configuration

The wallet profile has one API key today, `api.node_url`, read through
`glb.NodeAPIURL`; `api.endpoint` is its legacy name and stays readable. The
generated profile lists the public nodes under it as comment lines only.

One optional key is added, default empty:

```yaml
api:
    node_url: http://127.0.0.1:8000
    # witnesses: nodes asked to confirm the branch the library proof is anchored to.
    # Not used for anything else. An empty list disables the check with a warning.
    node_urls:
        - http://65.21.170.230:8001
        - http://79.137.70.25:8001
        - http://51.254.47.76:8001
```

Rules:

- The primary is still the only node every command talks to. The witnesses are
  consulted for one thing, the branch ID. No failover, no load balancing: a
  witness being down never breaks a command.
- The working list is `node_url` plus `node_urls`, deduplicated, with the
  primary removed. Whoever runs their own node keeps the public nodes as
  witnesses, which is the trust split wanted.
- `proxi config wallet` fills `node_urls` with every node of the public table
  in `proxi/config_cmd/public_nodes.go`, not only the wallet-hint subset, and
  not as comments. A profile without the key, or with an empty list, behaves
  as today plus one warning per run saying the branch was not cross-checked;
  a standalone or local-net setup has one node and must keep working.
- The primary and the witnesses are URLs like `node_url`, read through one
  helper, `glb.WitnessURLs`, so the alias and deduplication live in one
  place. No flag; the list is configuration.

## 3. The check

```go
// proxi/glb/witness.go
func VerifyBranchWithWitnesses(branchID base.TransactionID) error
```

Called once per process from `glb.GetTxLibrary`, right after the library proof
has verified, with the `branch_id` of that proof. For each witness, in
parallel, with a short per-witness timeout (5 s):

1. Fetch the witness's latest reliable branch. No answer means the witness is
   unreachable; everything after this is a verdict.
2. Ask the witness for the branch chain ending at `branchID`,
   `GetBranchChainTo(branchID, branchID.Slot())`. The handler answers from the
   witness's committed branches and errors when it does not know the branch:
   the witness has not committed it. This is the guard against forgery. A
   committed branch is one a sequencer signed and the witness validated, which
   an attacker on the wallet's path cannot produce.
3. If the witness's reliable branch is past the slot of `branchID`, ask the
   witness for the chain from its reliable branch back to that slot,
   `GetBranchChainTo(witnessLRB, branchID.Slot())`, and require `branchID` in
   it. A branch the witness committed but does not have on its reliable lineage
   is a fork that lost. A witness at the same slot or behind cannot tell yet:
   two branches of one slot are both candidates until the next slot settles it,
   so step 2 alone decides. Requiring equality at the same slot was considered
   and rejected: slots fork often enough that it would fail honest wallets.

Verdict: every reachable witness must pass, and at least one must be reachable.
An unreachable witness is skipped with a warning naming it. No reachable
witness, or any witness that fails, aborts the command with a message naming
the primary, the witness and the two branch IDs. Zero configured witnesses is
the single warning of §2.

The check runs once because the library is fetched once. It costs two or three
small requests per witness.

## 4. What it does and does not close

- **One compromised path.** The attacker controls the connection to the
  primary. Witnesses on other paths have never seen the forged branch and the
  check fails. Closed.
- **One malicious node.** The primary is honest on the wire but lies. Same
  outcome as above. Closed, which transport security could never do.
- **A malicious witness.** It cannot make the check pass for a forged branch
  unless every other witness agrees; it can only make an honest branch fail,
  which is denial and visible in the message.
- **Every path the user has.** The attacker owns the user's router or ISP and
  answers for every endpoint. All witnesses agree with the primary and the
  check passes. Only TLS to a certificate the wallet trusts closes this. It is
  the documented residual of running the API on plain HTTP for take 1; if the
  public nodes move to HTTPS later, the witness list is where their URLs go.

## 5. Out of scope

- The wasm wallet. It is hosted by a page that chooses its own endpoints; the
  rule of §3 applies, the plumbing is the page's.
- Using the witnesses for anything beyond the branch ID. The latest reliable
  branch, balances and UTXOs come from the primary; disagreement about those
  is harmless by the input commitment.
- Changing what `get_branch_list` returns. Its cap on the response length is
  no concern here: the chain requested spans the few slots between the
  primary's branch and the witness's reliable one.

## 6. Tests

`proxi/glb/witness_test.go`: scripted witnesses behind the two-call interface
the check talks to, each a reliable branch, a set of committed branches and a
lineage. Cases: two ahead agree (pass), one at the same slot on a sibling fork
(pass), one lagging (pass), one does not know the branch (fail, names the
witness), one has it committed but off its reliable lineage (fail), one
unreachable and one agreeing (pass with warning), none reachable (fail); and
the list rule, primary removed, repeats and trailing slashes ignored.
