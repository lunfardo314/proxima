#!/bin/bash

echo "FOR TESTING PURPOSES ONLY! NOT SAFE"

bootstrapWallet="addressED25519(0x3faf090d38f18ea211936b8bcf19b7b30cdcb8e224394a5c30f9ba644f8bb2fb)"
# faucetAddress="addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"

#proxi db distribute $bootstrapWallet 100000 $faucetAddress 100000000

proxi db distribute $bootstrapWallet 100000
