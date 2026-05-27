# Chain explorer

## Context and goal

In Proxima _chained account_ or _chain_ in short, as a ubiquitous UTXO covenant, a first class citizen.

Sequencers, delegations, foundries - all of them run on chains.

So far, we were running less than 100 of chained accounts on the ledger. The projected numbers are thousands and hundreds of thousands. 

Currently, we only have tools to explore it with commands like `proxi node allchains`, `proxi node balance`, `proxi node chain` and similar.

We need a convenient browseer-based tooling to explore the variety of chains, query, filter it, explore details.

Te goal is to create one as an API endpoint of the node.

## Requirements

- browser based ledger explorer, focused on chained accounts.
- served by node as a API endpoint `/chain-explorer`
- displays chained accounts on page, max number is capped by user (normally few hundreds). No pagination of big sets
- have rich filtering controls. Some examples:
  - by controlling holder ID
  - by type (sequencer, foundry, delegation, generic(only chain constraint))
  - delegations by target, by master
  - active las N slots
  - token balance bounds
- the browser should be a list/table of chains with basic detail in the table: chainID, balance, transition counter, type, controller ID, UTXO ID, delegation target (if applicable)
- table elements should contain reasonable tooltip and/or links to other views. Some of them :
  - tooltip/link to the chain view with the controller ID
  - link to natural relatives, e.g. link/tooltip to delegation target 
  - details popup (or page). Type specific, that also contain links
  - etc TBD
  
Many of these views will require specific ways to traverse state DB and specific APIs. 
Current API may not be enough, so we need to implement what is needed on the server.

Normal mode is autorefresh it every, say, 10 sec. 