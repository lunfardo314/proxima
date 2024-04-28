# Proxima
Proxima is the node for  _distributed_, _decentralized_ and _permissionless_ UTXO ledger 
with DAG-based _cooperative_ and _leaderless_ consensus, also known as the _UTXO tangle_.

Please find [technical whitepaper](docs\Proxima_WP.pdf) here. 
The [TODO list](TODO.md) contains a constantly changing list of pending and ongoing development issues with their completion status.  

Proxima presents a novel architecture for a distributed ledger, commonly referred to as a "blockchain". 
The ledger of Proxima is organized in the form of directed acyclic graph (DAG) with UTXO transactions as vertices, 
rather than as a chain of blocks. 

Consensus on the state of ledger assets, is achieved through the profit-driven behavior of token holders which are the only
category of participants in the network. This behavior is viable only when they cooperate by following _biggest ledger coverage_ rule, 
similarly to the _longest chain rule_ in PoW blockchains. 

The profit-and-consensus seeking behavior is facilitated by enforcing purposefully designed UTXO transaction validity constraints. 
The participation of token holders in the network is naturally and completely permissionless. 
Proxima distributed ledger does not require miners, validators, committees or staking.

In the proposed architecture, participants do not need knowledge of the global state of the system or the total order of ledger updates. 
The setup allows to achieve high throughput and scalability alongside with low transaction costs, 
while preserving key aspects of decentralization, open participation, and asynchronicity found in Bitcoin and other proof-of-work blockchains, 
but without huge energy consumption. 

Sybil protection is achieved similarly to proof-of-stake blockchains, using tokens native to the ledger, 
yet the architecture operates in a leaderless manner without block proposers and committee selection.

Currently, the project is in an **experimental development stage**. 

The repository contains Proxima node prototype intended for experimental research and development. 
It also contains some tooling, which includes rudimentary wallet functionality.


