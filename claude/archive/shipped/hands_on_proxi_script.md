# Hands-on proxi test script (standalone node)

> **QUEUED → `tests/README.md`** — Repeatable manual smoke test of `proxi` against a local standalone node.
> Rewritten there, then archived. See `claude/kb_reorg.md`.

## Purpose & scope

A **quick, repeatable** manual smoke test of `proxi` against a real local
standalone node — no deep codebase analysis required. Run it to sanity-check
wallet flows (`send`, `compact`, sequencer withdraw) end-to-end, especially
after ledger/proxi changes.

This is intentionally **not exhaustive**. Extend it as new commands/flags need
coverage. The current version exercises: standalone deployment via the standard
`proxi config` commands, sequencer withdraw, `send` (plain / `--deadline` /
`--deadline --return`), the `--return` storage-deposit guard, and `compact`
(simple sweep + `returnToSender` skip path).

Companion deeper runbook: `one_node_bootstrap.md` (same bucket). Both are superseded by `tests/README.md`.

---

## Reference assets

- **`$WORK`** = a fresh scratch dir, e.g. `$CLAUDE_JOB_DIR/tmp/standalone`. The
  node MUST run from `$WORK` (reads `proxima.yaml`, `.snapshot`, key from cwd),
  and `proxi` commands read `proxi.yaml` from cwd.
- **Test wallet1 key** — a throwaway dev key (the genesis controller). Write it
  to `$WORK/proxima.key` so `proxi config wallet` reuses it instead of prompting
  for entropy:
  ```json
  {
    "version": 1,
    "key_type": 0,
    "private_key": "0173fcf78e9a10e58218e2aa69c96128bb4de11fbf8668c0c3c59e3f70d6a3469bd0e92e418554938bd70d6283aa59bf3cc02a9694079d3a0940c219da2cc7aa",
    "public_key": "9bd0e92e418554938bd70d6283aa59bf3cc02a9694079d3a0940c219da2cc7aa",
    "holder_id": "fb03128a43df116e1dcb372e436171f4c61c12ee54f0fd4d0652e56cf9323aa2"
  }
  ```
  - wallet1 holder ID: `fb03128a43df116e1dcb372e436171f4c61c12ee54f0fd4d0652e56cf9323aa2`
  - bootstrap sequencer ID (deterministic): `9d2c6fedeb0f31a9a97d28c59b276402f6c8e78777b89a82`
  - node API: `http://127.0.0.1:8000`
- **PTY helper** — `proxi config wallet` (new key) and `proxi config node`
  (libp2p host key) need a terminal for entropy and hard-fail when stdin isn't a
  TTY. Drive them through a pseudo-terminal, feeding a seed line on stdin:
  ```bash
  pty() { printf '%s\n' "hands-on-seed-0123456789" | script -qec "$*" /dev/null; }
  ```

---

## 0. Build binaries

```bash
go build -o "$WORK/proxima" .          # node (main.go at repo root)
go build -o "$WORK/proxi"   ./proxi    # CLI
```

## 1. Deploy standalone config + genesis (standard `proxi config` commands)

```bash
cd "$WORK"
# write the embedded test key first so 'config wallet' reuses it (no entropy prompt)
cat > proxima.key <<'KEY'
{ "version":1, "key_type":0,
  "private_key":"0173fcf78e9a10e58218e2aa69c96128bb4de11fbf8668c0c3c59e3f70d6a3469bd0e92e418554938bd70d6283aa59bf3cc02a9694079d3a0940c219da2cc7aa",
  "public_key":"9bd0e92e418554938bd70d6283aa59bf3cc02a9694079d3a0940c219da2cc7aa",
  "holder_id":"fb03128a43df116e1dcb372e436171f4c61c12ee54f0fd4d0652e56cf9323aa2" }
KEY

./proxi config wallet -f                       # reuses proxima.key -> writes proxi.yaml
pty "./proxi config node --standalone -f"      # writes proxima.yaml AND the genesis .snapshot
```
Expected: `config wallet` prints `Using existing key file 'proxima.key'` and
creates `proxi.yaml` (api `127.0.0.1:8000`, `default_sequencer_id` +
`wallet.sequencer_id` = the bootstrap ID). `config node --standalone` creates
`proxima.yaml` (sequencer `boot`, `enable: true`, `standalone: true`,
`controller_key_file: proxima.key`) **and** writes
`s0-0-…snapshot` (no separate `init genesis` needed). Always re-deploy from a
fresh `$WORK` after a breaking ledger change — the library hash changes, so an
old snapshot/DB won't load.

## 2. Start the node (background) and wait for branches

```bash
cd "$WORK" && ./proxima > startup.log 2>&1   # run in background
```
Poll until ready (~20-40s): `./proxi node info` should print an LRB branch id, a
`sequencer: $/9d2c6fed…` line, and `slot 0: <library-hash> … IN EFFECT`. The
node log shows `SUBMIT BRANCH s…` lines from `[SEQ:boot]`.

## 3. Fund wallet1 from the sequencer

```bash
./proxi node balance                       # ~1B on the bootstrap chain, 0-1 non-chain
./proxi node seq withdraw 500000000 -f     # moves 500M to a plain sigLock output
```
Expected: withdraw tx tracked to inclusion depth 1; `node balance` afterwards
shows a non-chain output of ~500M.

## 4. `send` tests

```bash
SELF=fb03128a43df116e1dcb372e436171f4c61c12ee54f0fd4d0652e56cf9323aa2
```

(A) plain send to self:
```bash
./proxi node send 80000000 -t a/$SELF -f -n
```
Expected: `mode: plain transfer (target lock is sigLock)`; tx submitted.

(B) sendWithDeadline:
```bash
./proxi node send 90000000 -t a/$SELF --deadline -f -n
```
Expected: `mode: sendWithDeadline (acceptance=60 slots, cleanup=8000 slots)`.

(C) sendWithDeadline + returnToSender (the new flag). Needs a different target,
so first create wallet2 in its own dir (see §5 (F)) and use its holder ID `W2`:
```bash
./proxi node send 200000000 -t a/$W2 --deadline --return 100000000 -f -n
```
Expected: extra line `return: target must return 100_000_000 to a/<self> to
accept`; tx submitted. `node utxo` then shows a `sendWithDeadline` output of
200M.

(D) returnToSender storage-deposit guard (must refuse):
```bash
./proxi node send 200000000 -t a/$W2 --deadline --return 1000 -f -n
```
Expected: NO tx; error
`--return 1_000 is below the return receipt's minimum storage deposit
9_500_000; the target could never accept this output`.

## 5. `compact` tests

(E) compact as wallet1 (master of the SWD outputs):
```bash
./proxi node compact -f
```
Expected: sweeps only the plain sigLock outputs (e.g. 80M + change) into ONE
sigLock output; the `sendWithDeadline` outputs (wallet1 is master, before the
acceptance window) and the chain output are left untouched. Tx included — it
does **not** choke on the SWD/returnToSender outputs.

(F) wallet2 + compact as the returnToSender target. Create wallet2 in its own
dir (the `-c` profile flag is broken — see Gotchas — so use a separate cwd whose
default `proxi.yaml` is wallet2):
```bash
mkdir -p "$WORK/w2" && cp "$WORK/proxi" "$WORK/w2/" && cd "$WORK/w2"
pty "./proxi config wallet -f"                 # fresh key + proxi.yaml (api + bootstrap seq auto-filled)
W2=$(grep holder_id proxi.yaml | awk '{print $2}')   # use this as the target in §4 (C)/(D)
# ... run §4 (C) from wallet1 to a/$W2 first, then back here:
./proxi node compact -f -v
```
Expected: the classifier flags the incoming SWD+returnToSender as `NeedsReturn`:
```
skipping 1 output(s) carrying returnToSender — claiming them requires sending a
return receipt to the master, which compact does not build.
  they become ordinary outputs once the master reclaims them after the deadline;
  re-run compact then.
nothing to compact (0 simply-claimable output(s))
```
With unrecognized-structure outputs present, compact instead prints
`refusing N output(s) with unrecognized structure …` and (under `-v`) lists
them.

## 6. Cleanup

```bash
pkill -f "$WORK/proxima"
rm -rf "$WORK"
```

---

## Gotchas (learned the hard way)

- **Fresh `$WORK` after breaking ledger changes.** A snapshot/DB from an older
  library hash won't load. Re-running §1 from an empty `$WORK` regenerates.
- **`config node` / `config wallet` (new key) need a TTY** for entropy and
  hard-fail otherwise. Use the `pty()` helper (`script -qec … /dev/null` with a
  seed on stdin). `config wallet` skips the entropy prompt when `proxima.key`
  already exists, so wallet1 (reusing the embedded test key) needs no PTY.
- **`config node --standalone` also creates the genesis snapshot** — there's no
  separate `proxi init genesis` step (`proxi init` only has `genesis` left, and
  it's redundant here).
- **`proxi -c/--config <profile>` is ignored** — it always loads `./proxi.yaml`.
  Run each wallet from its own directory whose default `proxi.yaml` is that
  wallet. (Tracked in `claude/TODO.md`.)
- **Run the node from its working dir**; `proxi` commands too (cwd-relative
  config).
- Wait ~30s after node start before issuing commands (sequencer needs to make
  the first branches). Use `-n` on sends to skip inclusion-tracking, `-f` to
  skip prompts.
