#!/bin/bash
# Starts the Proxima node from the current directory.
# If proxima.key is encrypted, prompts for the passphrase and passes it
# to the node via the SEQUENCER_KEY_PASSPHRASE environment variable.

if [ ! -f proxima.key ]; then
    echo "Error: proxima.key not found in current directory" >&2
    exit 1
fi

if grep -q '"crypto"' proxima.key; then
    read -s -p "Enter passphrase for proxima.key: " PASSPHRASE
    echo
    if [ -z "$PASSPHRASE" ]; then
        echo "Error: passphrase cannot be empty" >&2
        exit 1
    fi
    SEQUENCER_KEY_PASSPHRASE="$PASSPHRASE" exec proxima
else
    exec proxima
fi
