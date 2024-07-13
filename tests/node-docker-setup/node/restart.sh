#!/bin/sh


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


kill_proxima
sleep 2  # let process die

./proxima &
