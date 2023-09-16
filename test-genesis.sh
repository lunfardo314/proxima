#!/bin/bash

echo "FOR TESTING PURPOSES ONLY! NOT SAFE"
echo "Today is " `date`
echo "creating test genesis for the Proxima node"

dbname="proximadb"
txStoreDb="proximadb.txstore"

echo "state database name: " $dbname
echo "tx store database name: " $txStoreDb

faucetPrivateKey="f7da740869b90e9b4ba9577aa73a28ac0643c32cab6936c6b32017e16436155e3646b53da49b02e1cf6f1445f5ba5e840ddd7087d11522a8af14faaa8a60278b"
faucetAddress="addressED25519(0x571ff508f4fda468972ce8b22a26775f7b0a7a65572236c87741d3ad473a6d0a)"

echo "faucet address is assumed: " $faucetAddress

