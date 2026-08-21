#!/bin/bash

if [ ! -f "proxi" ]; then
    # we need local proxi for update-snapshot.sh, build the image
    ./build.sh
fi 

docker compose up
