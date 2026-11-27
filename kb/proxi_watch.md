# Watch list in `proxi`

> **BUILT, revision 2, merged into `develop` 2026-11-27 (not yet in a release)** (cut 2026-11-27 from `develop` `34cbc2fa`, v1.8.0). Revision 1, the `proxi node balance --summary` flag, was built and checked against the take 1 network (`8e62750b`); revision 2, approved the same day, makes it a command of its own, `proxi node summary`: built and checked against the take 1 network (watch list, `-v --rw`, `-t`, a chain lock refused). Wallet side only: `proxi/glb`, `proxi/node_cmd`, the wallet profile template. No node, API or ledger change, no hardfork.

---

## 1. What and why

An operator holds funds under several keys: a wallet, a cold wallet, the controller keys of a few sequencers, other wallets. `proxi node balance` shows one account at a time, so seeing the whole means running it once per key and adding up by hand.

The watch list names a set of holder IDs in the wallet profile, and `proxi node summary` shows them side by side with a total. Nothing in it needs a private key: it only reads the state.

## 2. The profile

A new top-level key `watch` in `proxi.yaml`, a mapping of names to holder IDs:

```yaml
watch:
    boot: <holder ID>
    oseq1: <holder ID>
    wallet1: <holder ID>
```

- A holder ID is 64 hex characters (32 bytes), with or without the `a/` prefix the `--target` flag also accepts.
- Two entries are implied and come first in the list:
  - `wallet`: the holder ID of the public key in `wallet.key_file`, when that key is configured. Read from the keystore's public key, so no passphrase is asked.
  - `cold`: `cold_wallet.holder_id` (`kb/cold_wallet.md`), when configured.
- Errors, each stopping the command before any request to the node:
  - a name given twice;
  - a name `wallet` or `cold` in the `watch` section, since those are taken by the implied entries;
  - one holder ID under two names, the implied entries included;
  - a value that is not a holder ID;
  - `watch` present but not a mapping.
- The order of the display is the order of the file, after `wallet` and `cold`.
- An empty or missing `watch` section is not an error: the list is then the implied entries alone. With no entry at all the command stops with an error.

The section is read straight from the profile file with `gopkg.in/yaml.v3` into a node tree, not through viper: viper lowercases keys and loses the order. A repeated key makes viper refuse the whole profile, and `glb.ReadInConfig` ignores that error, so with a duplicate in `watch` every profile key reads as unset; the node tree keeps both entries and the watch list reports the duplicate before anything is sent. Names are compared exactly as written. Code: `proxi/glb/watch.go`, `proxi/node_cmd/summary.go`.

The wallet profile template gets the section commented out, with one line on what it is for.

## 3. The command

`proxi node summary`, a command of its own beside `proxi node balance`, which stays as it is on `develop` (shows everything one account holds). No arguments. Flags:

- `-v` (the global verbosity flag): one line per delegation, see below, and holder IDs in full.
- `-t`, `--target <holder ID>`: the summary of that one account instead of the watch list, under the name `target`, in the same layout. Same meaning as on every other command (this account instead of the wallet), same forms (`a/...` or bare 64 hex). A chain lock (`c/...`) is an error: the summary is per holder ID. The watch list is not read, so an error in the `watch` section does not stop it.
- `--lrb-depth N`: read the state N branches back from the LRB, for every entry, as in `balance`.
- `-w`, `--rw`: with `-v`, sort each entry's delegations by the end of their safe revocation window instead of by balance, as in `balance`.

The flags are read from their own variables, not through viper, since `balance` registers `lrb-depth` and `rw` too and a viper key bound from two command trees keeps only the last registration.

For each entry, one request: `GetOutputsForControllerID(holder, LockTypeAll, MaxOutputs: GetOutputsIterationCap, LRBDepth)`, the same one `balance` makes. A partial result (iteration cap hit) is flagged on the entry's line. Each request reads the LRB at that moment, so the LRB is printed once and a warning is printed when the entries were not all read on the same branch.

Of the outputs returned for a holder H, two kinds are counted:

- **sigLock outputs**: the lock is `sigLock` and its holder (index value 0) is H. A sequencer's chain output is one of these, so a sequencer controller's balance includes its chain; delegations, tag-alongs and send-with-deadline outputs are not.
- **Delegations**: outputs that parse as a delegation with master H, wherever their target. Their balance is counted in full, as in `balance`.

Anything else indexed under H (outputs others indexed there, tag-alongs, deadlines) is left out of the summary; `proxi node balance -t H` shows it. The two counts follow the same rule as `proxi node holders` (a sigLock output belongs to its holder, a delegation to its master), but read the account index and are complete, while `holders` walks the UTXO set under a cap (20,000 on the public nodes) and may miss outputs.

### Normal mode

The header, a dashed line across the table, one line per entry, another dashed line and a total line:

```
name     holder         sigLock outputs              delegations                    total
-----------------------------------------------------------------------------------------------
wallet   a1b2c3d4..     12   1,234.000000 PROX       3   30,000.000000 PROX       31,234.000000 PROX
...
-----------------------------------------------------------------------------------------------
TOTAL                   40   ...                     11  ...                      ...
```

The holder is shown in its short form; `-v` prints it in full.

### Verbose mode (`-v`)

Under each entry, one line per delegation mastered by it, by balance descending (`--rw`: by the end of the safe revocation window, ascending):

- the delegation's chain ID;
- its balance;
- `->` and the target sequencer's name, padded with spaces to 8 characters, and its full chain ID. Names come from one request for all sequencer outputs (`glb.FetchSequencerCandidates`, active and inactive); a target not among them is shown as `?`;
- its freeze status, the same text as `balance` shows (`glb.DelegationStatusString`: frozen until slot S, in the safe revocation window until slot S, on hold, or unlockable by the master).

```
      $/4a89a9ada803710139efaacd54a87b7124a659c2ade8deeb     2,420,963.670437 PROX  -> boot     $/50726f78696d612e626f6f7473747261702e636861696e2e  frozen until slot 360321 (62 hours, 58 min from now)
```

## 4. Not in scope

- Native tokens, foundries, non-sequencer chains and the askstop cost: `balance` per account shows them.
- Watching continuously (a refresh loop) and alerts.
- Chain locks (`c/...`) as entries: the list is holder IDs only.
