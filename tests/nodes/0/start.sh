#!/bin/bash

INITIALIZED_FILE="/initialized"

# Parameters
SOURCE_PARAM=$1

# cp 0/*.yaml .
# Define source directories
SOURCE1="0"
SOURCE2="1"
SOURCE3="2"

echo $SOURCE_PARAM

# Copy files from selected source directory to target directory
cp -r ./"$SOURCE_PARAM"/*.yaml .

if [ ! -f "$INITIALIZED_FILE" ]; then
    echo "image not initialized"

    ./proxi init genesis_db

    if [ "$SOURCE_PARAM" == "0" ]; then
        ./proxi init bootstrap_account
    fi
fi

./proxima &


if [ ! -f "$INITIALIZED_FILE" ]; then
    if [ "$SOURCE_PARAM" == "0" ]; then
        sleep 10
        ./proxi -f node sequencer withdraw --finality.weak 800000000000000
        
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0x62c733803a83a26d4db1ce9f22206281f64af69401da6eb26390d34e6a88c5fa)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0x24db3c3d477f29d558fbe6f215b0c9d198dcc878866fb60cba023ba3c3d74a03)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0xaad6a0102e6f51834bf26b6d8367cc424cf78713f59dd3bc6d54eab23ccdee52)"
    fi
    
    # Create the initialized file to mark the container as initialized
    touch "$INITIALIZED_FILE"
fi

# Infinite loop
while true; do
    sleep 5
done
