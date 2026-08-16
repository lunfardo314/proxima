#!/bin/sh

# start script for all nodes
# 

INITIALIZED_FILE="/initialized"

# Parameters
NODE_NAME=$1


# increase the maximum buffer for quic
#sysctl -w net.core.rmem_max=7500000
#sysctl -w net.core.wmem_max=7500000

if [ ! -f "$INITIALIZED_FILE" ]; then
    # node not initialized
    echo "image not initialized"
    
    # Copy files from selected node directory to target directory
    cp -r ./"$NODE_NAME"/*.yaml .
    cp -r ./"$NODE_NAME"/*.key .


    if [ "$NODE_NAME" = "boot" ]; then
        # start proxima on boot node
        ./proxima &
        PROXIMA_PID=$!
        sleep 20  # let process and sequencer start

        # distribute funds
        ./proxi node seq withdraw 2000010000000 -f

        ./proxi node fund -f
    elif [ "$NODE_NAME" = "1" ] || [ "$NODE_NAME" = "2" ] || [ "$NODE_NAME" = "4" ]; then
        echo "setup sequencer"

        ./proxima &
        PROXIMA_PID=$!
        #sleep 5  # let process start
        sleep 30

        echo "node init sequencer"
        # Loop until the command succeeds
        while true; do
            ./SetupSequencer seq$NODE_NAME 499990000000
            
            # Check if the command was successful
            if [ $? -eq 0 ]; then
                echo "Command executed successfully."
                break
            else
                echo "Command failed, retrying in 5 seconds..."
                sleep 5
            fi
        done

        kill -TERM $PROXIMA_PID; wait $PROXIMA_PID
        sleep 5
        ./proxima &
        PROXIMA_PID=$!
    else
        ./proxima &
        PROXIMA_PID=$!
    fi 

    # Create the initialized file to mark the container as initialized
    touch "$INITIALIZED_FILE"
else # initialized

    ./proxima &
    PROXIMA_PID=$!
fi

# For the initialized branch (first boot), proxima is also running
# so we need the same trap there too -- see note below
trap 'echo "Shutting down proxima $PROXIMA_PID..."; kill -INT $PROXIMA_PID; wait $PROXIMA_PID; exit 0' INT TERM
wait $PROXIMA_PID
