#!/bin/bash

if [ ! -f "proxi" ]; then
    # we need local proxi for update-snapshot.sh, build the image
    ./build.sh
fi 

if [ ! -d "./data/config" ]; then
    mkdir -p ./data/config
    chmod -R 777 ./data
fi


docker compose up
