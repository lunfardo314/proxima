#!/bin/bash

# start script for all nodes
# 

INITIALIZED_FILE="/initialized"

# Parameters
NODE_NAME=$1

# Copy files from selected node directory to target directory
cp -r ./"$NODE_NAME"/*.yaml .

kill_proxima() {
    # Find the PID of proxima
    pid=$(ps aux | grep proxima | grep -v grep | awk '{print $2}')

    # Check if the PID variable is not empty
    if [ -n "$pid" ]; then
        echo "Killing proxima $pid"
        kill $pid
    else
        echo "Proxima process not found"
    fi
}

if [ ! -f "$INITIALIZED_FILE" ]; then
    # node not initialized
    echo "image not initialized"

    echo "init genesis_db"
    ./proxi init genesis_db

    if [ "$NODE_NAME" == "boot" ]; then
        # 
        echo "init bootstrap_account"
        ./proxi init bootstrap_account

        ./proxima &

        sleep 10
        ./proxi -f node sequencer withdraw --finality.weak 800000000000000
        
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0xaa401c8c6a9deacf479ab2209c07c01a27bd1eeecf0d7eaa4180b8049c6190d0)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0x62c733803a83a26d4db1ce9f22206281f64af69401da6eb26390d34e6a88c5fa)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0x24db3c3d477f29d558fbe6f215b0c9d198dcc878866fb60cba023ba3c3d74a03)"
        ./proxi -f node transfer 200000000000000 --finality.weak -t "addressED25519(0xaad6a0102e6f51834bf26b6d8367cc424cf78713f59dd3bc6d54eab23ccdee52)"

        touch "$INITIALIZED_FILE"

        # Infinite loop
        while true; do
            sleep 5
        done
    fi
    if [ "$NODE_NAME" == "1" ] || [ "$NODE_NAME" == "4" ]; then
        echo "setup sequencer"

        ./proxima &
        sleep 5  # let process start

        ./proxi node setup_seq --finality.weak mySeq 100000000000000

        kill_proxima

    fi 

    # Create the initialized file to mark the container as initialized
    touch "$INITIALIZED_FILE"
fi


./proxima
