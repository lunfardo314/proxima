#!/bin/sh

# first try to fetch from local harddrive
if [ -f "./config/proxi.yaml" ]; then
    cp ./config/proxi.yaml ./proxi.yaml
fi

if [ -f "./config/proxima.key" ]; then
    cp ./config/proxima.key ./proxima.key
fi

if [ ! -f "./proxi.yaml" ]; then
    ./proxi config wallet    
fi

# copy always if not found
if [ ! -f "./config/proxi.yaml" ]; then
    cp ./proxi.yaml ./config/proxi.yaml
    chmod  777 ./config/proxi.yaml
fi

if [ ! -f "./config/proxima.key" ]; then
    cp ./proxima.key ./config/proxima.key
    chmod  777 ./config/proxima.key
fi

# first try to fetch from local harddrive
if [ -f "./config/proxima.yaml" ]; then
    cp ./config/proxima.yaml ./proxima.yaml
fi

if [ ! -f "./proxima.yaml" ]; then
    ./proxi config node --sequencer --name ???? -f
fi

# copy always if not found
if [ ! -f "./config/proxima.yaml" ]; then
    cp ./proxima.yaml ./config/proxima.yaml
    chmod  777 ./config/proxima.yaml
fi

if [ ! -d "./proximadb" ]; then
    ./proxi init genesis
fi

if [ -z "$(ls -A "./proximadb" 2>/dev/null)" ]; then
    # dir is empty
    ./proxi init genesis
fi

./proxima &
PROXIMA_PID=$!

# For the initialized branch (first boot), proxima is also running
# so we need the same trap there too -- see note below
trap 'echo "Shutting down proxima $PROXIMA_PID..."; kill -INT $PROXIMA_PID; wait $PROXIMA_PID; exit 0' INT TERM
wait $PROXIMA_PID
