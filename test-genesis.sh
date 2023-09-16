#!/bin/bash

echo "FOR TESTING PURPOSES ONLY! NOT SAFE"
echo "Today is " `date`
echo "creating test genesis for the Proxima node"

faucetAddress="addressED25519(0x571ff508f4fda468972ce8b22a26775f7b0a7a65572236c87741d3ad473a6d0a)"

proxi db genesis
proxi db distribute $faucetAddress 10000000
