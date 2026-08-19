#!/bin/bash

# URL of the directory with snapshot files, without the trailing slash.
URL="${SNAPSHOT_DIR_URL:?set SNAPSHOT_DIR_URL to the snapshot directory URL}"
LOCAL_SAVE_PATH="./data/"


TIMEOUT=60  # Timeout in seconds
INTERVAL=5   # Interval between retries (in seconds)

START_TIME=$(date +%s)
NEXT_OK_FILE=""

if [ ! -f "proxi" ]; then
    echo "proxi not found locally. fetching it from the container..."
    docker create --name temp_container_proxima proxima-node
    docker cp temp_container_proxima:/app/proxi .
    docker rm temp_container_proxima
fi

# Retry mechanism in case the latest snapshot file is currently written. Wait for the export to finish
while true; do
    echo "searching for valid file..."
    # Fetch the directory listing and get the most recent file that doesn't start with __tmp__
    RECENT_FILE=$(curl -L -s $URL | grep -Eo 'href="[^"]+"' | sed 's/href="//g' | sed 's/"//g' | grep -v "/$" | grep -v "^__tmp__" | tail -n 1)

    # Check the elapsed time
    CURRENT_TIME=$(date +%s)
    ELAPSED_TIME=$((CURRENT_TIME - START_TIME))

    # If a valid file is found, store it in NEXT_OK_FILE
    if [ -n "$RECENT_FILE" ]; then
        NEXT_OK_FILE=$RECENT_FILE
        echo "Valid file detected: $NEXT_OK_FILE"
        break
    fi

    # If timeout is reached, use the next valid file (if found)
    if [ $ELAPSED_TIME -ge $TIMEOUT ]; then
        if [ -n "$NEXT_OK_FILE" ]; then
            echo "Timeout reached. Using the next valid file found: $NEXT_OK_FILE"
        else
            echo "Timeout reached. No valid file found, skipping download."
            exit 0
        fi
        break
    fi

    echo "Temporary file detected, retrying..."
    sleep $INTERVAL
done

# Download the valid file (if available)
if [ -n "$NEXT_OK_FILE" ]; then
    wget "${URL}/${NEXT_OK_FILE}" -P "${LOCAL_SAVE_PATH}"
    echo "Downloaded file: ${NEXT_OK_FILE}"

    sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb.txstore"
    sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb"

    original_path=$(pwd)
    cd "${LOCAL_SAVE_PATH}"
    ./../proxi snapshot restore "${RECENT_FILE}"
    cd "$original_path"
fi
