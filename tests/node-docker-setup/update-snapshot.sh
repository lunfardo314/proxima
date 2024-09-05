#!/bin/bash

# URL of the directory (without the trailing slash)
URL="http://83.229.84.197/shared"

# Fetch the directory listing, sort by modification date, and extract the most recent file
#RECENT_FILE=$(curl -s $URL | grep -Eo 'href="[^"]+"' | sed 's/href="//g' | sed 's/"//g' | grep -v "/$" | grep -v "index.html" | sort | tail -n 1)
RECENT_FILE=$(curl -s $URL | grep -Eo 'href="[^"]+"' | sed 's/href="//g' | sed 's/"//g' | grep -v "/$" | tail -n 1)

# If a file is found, download it
if [ -n "$RECENT_FILE" ]; then
    echo "Downloading the most recent file: $RECENT_FILE"
    wget "${URL}/${RECENT_FILE}" -P ./data/${RECENT_FILE}
else
    echo "No files found in the directory."
fi

sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb.txstore"
sudo rm -rf "${LOCAL_SAVE_PATH}/proximadb"
cd "${LOCAL_SAVE_PATH}"
proxi snapshot restore "${RECENT_FILE}"
cd ..
