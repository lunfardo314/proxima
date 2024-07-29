#!/bin/sh

if [ ! -f "./proxi.yaml" ]; then
    ./proxi init wallet    
fi

if [ ! -f "./proxima.yaml" ]; then
    ./proxi init node -s
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


