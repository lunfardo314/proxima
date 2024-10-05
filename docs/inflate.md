## Inflation. Creating tokens out of thin air with `proxi` 

Proxima ledger has inflation. In theory, every token holder can inflate their holdings, i.e. create tokens out of thin air.

In practice, however, there certain parameters who, how and when can create positive inflation. 
The constraint is: new tokens can be created only on outputs called **chain outputs**. 

For those who know IOTA's UTXO ledger, Proxima's _chain outputs_ are similar to IOTA's _alias outputs_.

Chain output is an output (UTXO) with special script on it, called _chain constraint_.
It has unique ID called _chain ID_. The _chain ID_ is assigned by the ledger upon creation of the _chain origin_.
Chain outputs are consumed and produced in chains of transactions, each with chain output with the same _chain ID_.

Chain output, like any other output, contain certain amount of tokens on it. It s called _on-chain balance_.

New tokens (inflation) are created on the _successor_ chain output and inflation amount is proportional to the _on-chain balance_
of the _predecessor_ and to ledger time (in ticks) between predecessor and successor. Time is money.

To inflate your holdings, you create a chain using command `proxi node mkchain, <on-chain balance>`. 

The command creates chain origin with `<on-chain balance>` tokens on it. The newly assigned _chain ID_ is displayed.
One can always display all chains controlled by the wallet's private key with command `proxi node chains`.

To destroy the chain and return all tokens to the usual address command `proxi node delchain <chain ID>` can be used.

Note, that each of these commands will require certain tag-along fee paid to the sequencer of choice.

Proxima ledger (testnet version) has maximum total annual inflation capped by fixed amount. 
In the first year it is approximately 10% of the initial supply. Initial supply is equal to `1.000.000.000.000.000` tokens,
so maximum annual inflation is approximately `100.000.000.000.000`.

Given these numbers, it means that on each chain transaction maximum possible inflation is rather small.

For example `1.000.000.000` (1 billion) tokens will generate inflation ~350 tokens per each 10 slots (1.5 minutes), or about 
35 tokens per slot.

Smaller amounts on chain will generate 0 tokens of inflation. It puts natural limits of how big/small 
token holdings are meaningful for the inflation. 

Use command `proxi node inflate <chain ID>` to start inflating your on-chain balance. 

The command will start issuing chain transactions every 10 slots and create new tokens on it. 
Flag `-s <slots>` allows to change default number of slots (10) to any number from 2 and uo to 12. 
Note, that each of these transactions will cost a fee, so it does not make sense to issue them too often. 
Besides, 12 is maximum number of slots which allows positive inflation: it is so-called _inflation opportunity window_.

So, token holdings at least `100.000.000` and command `proxi node inflate <chain ID>` is close to optimal.x