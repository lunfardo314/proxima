#!/bin/bash

if [ ! -d "./data" ]; then
    mkdir ./data
    ./update-snapshot.sh
fi

if [ ! -d "./data/proximadb" ]; then
    mkdir ./data/proximadb
fi

if [ ! -d "./data/config" ]; then
    mkdir ./data/config
fi

docker compose up
