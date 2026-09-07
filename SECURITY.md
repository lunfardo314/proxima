# Reporting a vulnerability

Proxima is experimental software running a pre-launch network that exists, in part, to
find bugs. If you find one that can be exploited, please report it privately first.

**Where:** e-mail [lunfardo314@gmail.com](mailto:lunfardo314@gmail.com).

Do not open a public issue, and do not post it on Discord or elsewhere, until a fix is
on `develop` or 30 days have passed, whichever comes first.

## What counts

Anything that lets someone:

- mint tokens outside the ledger rules, or spend tokens they do not control;
- crash, wedge, or exhaust the memory or disk of a node from the network or the API;
- stall the network, or make nodes disagree on committed state;
- steer a wallet or a node onto a fabricated ledger.

Bugs that need the operator's own key or config, and denial of service that costs the
attacker more than the victim, are welcome too but are not urgent.

## What to expect

- An acknowledgement within a few days.
- A fix on `develop`, and a note in the commit message. Credit to you if you want it.
- No bounty. There is no money behind this project and no token has value; nothing here
  creates an obligation to pay anyone anything.

## Scope

The `develop` branch of this repository, the `easyfl` and `unitrie` libraries it depends
on, and the nodes listed on the
[documentation site](https://lunfardo314.github.io/#/participate/launch_network).

The pre-launch network is a throwaway: it may be reset at any time, including to deploy a
fix. Do not test against it in a way that harms other participants; a private network is
easy to run ([standalone node](https://lunfardo314.github.io/#/participate/run_standalone),
[Docker network](tests/docker/docker-network.md)).
