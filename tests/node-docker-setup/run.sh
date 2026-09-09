#!/bin/bash

./build.sh

if [ ! -d "./data/config" ]; then
    mkdir -p ./data/config
    chmod -R 777 ./data
fi

docker compose up
