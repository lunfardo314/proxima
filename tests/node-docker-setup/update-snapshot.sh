#!/bin/bash

# Variables
REMOTE_USER="hd"                # Remote username
REMOTE_HOST="185.139.228.149"   # Remote host IP or domain
REMOTE_DIR="/home/hd"           # Remote directory to search
LOCAL_SAVE_PATH="./data"  # Local path where the file will be saved

# Check if local save directory exists
if [ ! -d "$(dirname "$LOCAL_SAVE_PATH")" ]; then
    echo "Error: Local save directory does not exist."
    exit 1
fi

if [ ! -d "${LOCAL_SAVE_PATH}" ]; then
    mkdir "${LOCAL_SAVE_PATH}"
fi

# Find the most recent file in the remote directory
RECENT_FILE=$(ssh "${REMOTE_USER}@${REMOTE_HOST}" "ls -t ${REMOTE_DIR} | head -n 1")

if [ -z "$RECENT_FILE" ]; then
    echo "No files found in the remote directory."
    exit 1
fi

# Full remote file path
REMOTE_FILE_PATH="${REMOTE_DIR}/${RECENT_FILE}"

echo "Most recent file found: ${REMOTE_FILE_PATH}"

# Download the most recent file
scp "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_FILE_PATH}" "${LOCAL_SAVE_PATH}"

# Check if the scp command was successful
if [ $? -eq 0 ]; then
    echo "File downloaded successfully to ${LOCAL_SAVE_PATH}"
else
    echo "Failed to download file from ${REMOTE_HOST}"
    exit 1
fi

sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb.txstore"
sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb"
cd "${LOCAL_SAVE_PATH}"
proxi snapshot restore "${RECENT_FILE}"
cd ..
