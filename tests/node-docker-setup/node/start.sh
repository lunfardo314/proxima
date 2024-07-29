#!/bin/sh

# first try to fetch from local harddrive
if [ -f "./config/proxi.yaml" ]; then
    cp ./config/proxi.yaml ./proxi.yaml
fi

if [ ! -f "./proxi.yaml" ]; then
    ./proxi init wallet    
    cp ./proxi.yaml ./config/proxi.yaml
fi

# first try to fetch from local harddrive
if [ -f "./config/proxima.yaml" ]; then
    cp ./config/proxima.yaml ./proxima.yaml
fi

if [ ! -f "./proxima.yaml" ]; then
    ./proxi init node -s
    cp ./proxima.yaml ./config/proxima.yaml
fi

if [ ! -d "./proximadb" ]; then
    ./proxi init genesis_db
fi

if [ -z "$(ls -A "./proximadb" 2>/dev/null)" ]; then
    # dir is empty
    ./proxi init genesis_db
fi

./proxima &

# do not let the script end
while true; do
    sleep 1
done


