#!/bin/sh

# start script for all nodes
# 

INITIALIZED_FILE="/initialized"

# Parameters
NODE_NAME=$1


kill_proxima() {
    # Find the PID of proxima
    pid=$(ps aux | grep proxima | grep -v grep | awk '{print $1}')  # busy box format for 'ps aux'
    if [ -n "$BASH_VERSION" ]; then
        pid=$(ps aux | grep proxima | grep -v grep | awk '{print $2}') # bash format for 'ps aux'
    fi

    # Check if the PID variable is not empty
    if [ -n "$pid" ]; then
        echo "Killing proxima $pid"
        kill $pid
    else
        echo "Proxima process not found"
    fi
}

boot_param=""
if [ "$NODE_NAME" = "boot" ]; then
boot_param="boot"
fi
if [ ! -f "$INITIALIZED_FILE" ]; then
    # node not initialized
    echo "image not initialized"
    
    # Copy files from selected node directory to target directory
    cp -r ./"$NODE_NAME"/*.yaml .

    echo "init genesis_db"
    ./proxi init genesis_db

    if [ "$NODE_NAME" = "boot" ]; then
        # 
        echo "init bootstrap_account"
        ./proxi init bootstrap_account
    fi
    if [ "$NODE_NAME" = "1" ] || [ "$NODE_NAME" = "2" ] || [ "$NODE_NAME" = "4" ]; then
        echo "setup sequencer"

        ./proxima &
        sleep 5  # let process start

        echo "node init sequencer"
        # Loop until the command succeeds
        while true; do
            ./proxi node setup_seq --finality.weak seq$NODE_NAME 140000000000000
            
            # Check if the command was successful
            if [ $? -eq 0 ]; then
                echo "Command executed successfully."
                break
            else
                echo "Command failed, retrying in 5 seconds..."
                sleep 5
            fi
        done

        kill_proxima
        sleep 2  # let process die
    fi 

    # Create the initialized file to mark the container as initialized
    touch "$INITIALIZED_FILE"
fi

./proxima $boot_param

# do not let the script end
while true; do
    sleep 1
done
