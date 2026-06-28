#!/bin/sh

# first try to fetch from local harddrive
if [ -f "./config/proxi.yaml" ]; then
    cp ./config/proxi.yaml ./proxi.yaml
fi

if [ -f "./config/proxima.key" ]; then
    cp ./config/proxima.key ./proxima.key
fi

if [ ! -f "./proxi.yaml" ]; then
    ./proxi init wallet    
fi

# copy always if not found
if [ ! -f "./config/proxi.yaml" ]; then
    cp ./proxi.yaml ./config/proxi.yaml
fi

if [ ! -f "./config/proxima.key" ]; then
    cp ./proxima.key ./config/proxima.key
fi

# first try to fetch from local harddrive
if [ -f "./config/proxima.yaml" ]; then
    cp ./config/proxima.yaml ./proxima.yaml
fi

if [ ! -f "./proxima.yaml" ]; then
    ./proxi init node -s
fi

# copy always if not found
if [ ! -f "./config/proxima.yaml" ]; then
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


