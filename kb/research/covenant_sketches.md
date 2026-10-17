# Covenant sketches — a framework around ledger invariants

> **RESEARCH** — discussion of 2026-10-17, nothing built. Proposes a precise
> meaning for "covenant" (state, paths, transitions, invariant, escape hatches),
> a framing of L1 covenants as constraints on L2 entities, rules for composing
> covenants, and possibly a high-level language that is also a specification
> language. Prototypes: the tag-along, delegation and sequencing covenants.

## 1. Why

"Covenant" is used loosely for three different things: a piece of EasyFL code,
the behaviour that code enforces, and the guarantee its owner relies on. Keeping
them apart is most of the value of a framework. The idea is to build it around
the **ledger invariant**: what must stay true of an output and its lineage,
whoever builds the transactions that touch it. The constraint is then derived
from, and checked against, the invariant.

The finiteness of EasyFL is what makes this practical. A constraint has no loops
and no recursion (the library rejects cyclic call graphs, `easyfl/engine/recursion.go`),
reads only the transaction it judges and always terminates, so each one unfolds
into one finite formula over bounded byte strings. "Does every transaction this
constraint accepts preserve the invariant?" is then a question an SMT solver can
settle, proof or counterexample transaction, with no loop invariants to invent.

## 2. L1 covenants constrain L2 entities

The framing: **L1 covenants are constraints on L2 entities**, and those entities
are mostly centralized. A token holder is an L2 entity: one key, one party, its
own software. So is a sequencer operator, a rollup, an exchange, a miner. L1 does
not run them and does not coordinate them. It only decides which of the
transactions they write are valid.

Consequences:

- **Composition happens on L2; L1 enforces its correctness.** An L2 entity
  assembles a transaction from several covenants (consumes a tag-along, transits a
  delegation inside its own sequencer transaction, mints an inflation share). L1
  does not compose anything: it checks that every covenant involved accepts the
  result. The framework's job is to make sure that "every covenant accepts" implies
  "every covenant's invariant holds", whatever the composing entity intended.
- **Every L2 entity is a potential adversary of every covenant it touches.** A
  covenant's invariant has to hold against any transaction any entity can build,
  not against the transaction its author had in mind.
- The software that acts for a token holder (`proxi node mine`, the consolidator,
  a sequencer) is L2 software maximizing that holder's profit. The covenants are
  what make that profit-seeking safe for everyone else.

## 3. Tag-along: trustless asynchronous messaging between L2 entities

The tag-along lock (`ledger/def/lock_tag_along.easyfl`) already gives L1 a
messaging primitive, because the ledger enforces who sent the message.

- **The sender is authenticated by L1.** On the produced side the lock requires
  the sender field to equal the holder ID of the transaction's signer
  (`!!!sender_hash_check_failed`). Nobody can send a tag-along in somebody else's
  name.
- **The addressee is exclusive for a while.** On the consumed side only the target
  can take it in the first `constTagAlongSlots` (30) slots, by consuming its chain in
  the same transaction; then only the sender, until `constTagAlongReclaimSlots`
  (390); then anybody.
- **Payload rides along.** Other constraints on the same output carry the message:
  sequencer requests, the top-up request, askstop. The ensure constraints
  (`ledger/def/ensure.easyfl`) bind the consuming transaction to deliver what the
  request asks, and step aside at `constTagAlongSlots` so the sender's reclaim is
  never blocked.

Together that is **trustless asynchronous messaging and transacting on L1 between
L2 entities**: the receiver knows for certain who sent the message and what it
carries, the sender knows the message either reaches the addressee or comes back,
and neither has to trust the other or any third party. A request to a sequencer,
a payment with instructions, a call to a rollup are all the same pattern.

## 4. Centralized and decentralized assets

Most assets are owned by a centralized L2 entity: a `sigLock` output, a chain
controlled by one key, a delegation with a master. One private key decides.

But a covenant can also make an asset **decentralized**: consumable or transitable
by any L2 entity that satisfies its immutable conditions, with no private key at
all. The conditions are the owner. Existing examples:

- **The mine chain.** `mineLock` is an open lock (its own header says so): anyone
  can spend the mine output by satisfying the proof of work; the transaction's
  signature only identifies the miner, who is paid. Nobody owns the chain.
- **Abandoned tag-alongs.** After `constTagAlongReclaimSlots` anybody may consume
  one. A degenerate case, but the same shape: the condition is time.
- **DEX orders** (`ledger/def/lock_dex_orders.easyfl`). An order is owned by its
  maker but can be filled by any counterparty within its timeout, by meeting the
  price. Conditionally open.

Use cases to explore:

- **PoW-mineable foundries and native tokens.** A foundry whose minting follows the
  mine chain's pattern: anyone can mint by doing the work, by a schedule fixed at
  creation. A fair launch for any token, without a founder holding the supply.
- **Open sequencers.** A sequencer chain anybody may move, counting on the rewards
  for doing so. Open questions: who is paid and how, what protects the chain's
  capital (its invariant would have to be roughly "the capital only grows, the
  mover takes only a bounded reward"), and how coverage and endorsement choices
  are constrained when the mover has no stake.

A decentralized asset is where formal verification pays most: its invariant must
hold against every possible consumer, with no key holder to notice and react.

## 5. What a covenant is

A covenant would be defined by five parts:

| Part | What it is | Tag-along example |
|---|---|---|
| **State** | Typed fields kept in its output | amount, sender, target |
| **Paths** | The named ways the output may be consumed, each with its authority and its time window | target, sender reclaim, public |
| **Transitions** | Per path, what the consuming transaction must produce | none for the tag-along itself; the request constraints add obligations |
| **Invariant** | What the owner and every counterparty rely on, along the lineage | authenticated sender; nobody but the target before slot 30, nobody but target or sender before 390 |
| **Escape hatches** | Liveness: some path always stays open to the rightful party | at every slot somebody can consume it, and the sender always gets it back if the target does not take it |

Only the constraint runs on L1; the invariant is proven, never executed. That
keeps per-UTXO bytes minimal.

Escape hatches deserve to be first-class. The reclaim fix (ensure constraints
stepping aside at `constTagAlongSlots`) was a liveness bug, not a safety one, and
`kb/control_succession.md` exists to prevent a bricked sequencer chain. "No funds
can be bricked" should be a checked property.

## 6. Composability

Several covenants judge one transaction, and composing them means satisfying all of
them at once. That is sound only if no covenant can break another's invariant. The
prototypes already interact in every way that matters: a delegation transition
rides inside its target sequencer's transaction, a top-up is a tag-along consumed
into a delegation, and a request's ensure constraint obligates whoever consumes it.

Three ideas:

- **Footprints.** Each covenant owns its inputs, outputs and fields and constrains
  only those, plus what it declares. Disjoint footprints compose freely (the
  separation-logic idea).
- **Shared resources as accounting.** What is genuinely shared, token conservation,
  the inflation budget, the timestamp, is claimed in shares, and a ledger-level
  invariant checks that the claims add up. This is the boundary where the 2026-09
  native-token counterfeit happened: an aggregation driven by evaluating a builtin
  rather than by a structural scan. The framework should make resource claims a
  construct, so that mistake cannot be written.
- **Obligations versus invariants.** An obligation ("the consuming transaction must
  produce X") can only make a transaction impossible to build; it never breaks
  anybody's safety. So composition splits into two checks: safety, invariants hold
  under any combination; liveness, conflicting obligations never close an escape
  hatch.

## 7. A high-level language that is also the specification

One artefact per covenant: state, paths, transitions, invariant. The compiler emits

- EasyFL bytecode against the existing library, and
- proof obligations: per-transaction checks for an SMT solver, and inductive steps
  along the lineage (TLA+, k-induction) for multi-transaction properties.

The solver checks the emitted EasyFL itself, so the compiler need not be trusted
and hand-written EasyFL can be checked the same way.

Missing today: a translator from EasyFL formulas to SMT-LIB bit-vector terms; the
embedded builtins as uninterpreted functions with stated axioms (which requires
them to stay pure); a model of EasyFL's exact semantics, lazy `if` and byte
encodings included, not an idealized one.

## 8. Prototypes

The three covenants to work the framework out on, each autonomous, composable with
the others, and keeping its own invariant whatever the others do:

- **Tag-along** — the smallest, with an authentication property, three time-windowed
  paths and a liveness story. The first worked example.
- **Delegation** (`delegateLock`) — owner, target and top-up paths, freeze state,
  advance and share arithmetic; invariants over the delegation's whole lineage.
- **Sequencing** (chain and sequencer constraints, inflation) — chain identity and
  control, inflation claims as a shared resource; hosts the other two inside its
  transactions.

## 9. Migration without a hardfork

Renaming an EasyFL symbol is a hardfork, so the first step changes no bytes:
write specifications for the three existing covenants as they are, and verify
today's bytecode against them. That is the cheapest test of the framework, and it
may find bugs on the way. A covenant written in the new language comes after.

## 10. Risks

- **Leakage at the edges.** Byte-level semantics, lazy evaluation and the Go
  builtins are where the model and reality diverge.
- **Wrong specifications.** A covenant proven against the wrong invariant is worse
  than an unproven one, because it looks safe. Short, human-readable invariants
  matter more than the language's power.
- **Scope.** The value is in footprints, accounting, obligations and escape
  hatches. A full high-level language is optional; a specification language with a
  thin compiler may be enough.

## 11. Open questions

1. Do invariants range over one lineage, or over sets of lineages? "A sequencer's
   frozen delegations never exceed what its balance can advance" spans many chains.
2. Is the high-level language a superset of EasyFL with hand-written escapes, or a
   closed language that compiles to it?
3. What is the minimal set of builtin axioms a proof needs, and are all current
   builtins pure enough to state them?
4. For open (keyless) assets: what is the general shape of an invariant that holds
   against every consumer, and what reward makes moving them worthwhile without
   making them extractable?
