#!/bin/bash

if [ ! -d "./data" ]; then
    mkdir ./data
fi

if [ ! -d "./data/proximadb" ]; then
    mkdir ./data/proximadb
fi

if [ ! -d "./data/proximadb.txstore" ]; then
    mkdir ./data/proximadb.txstore
fi

docker compose up
