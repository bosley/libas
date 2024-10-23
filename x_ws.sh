#!/bin/bash

# Check if clientID is provided
if [ $# -lt 1 ]; then
    echo "Usage: $0 <clientID>"
    echo "Example: $0 550e8400-e29b-41d4-a716-446655440000"
    exit 1
fi

CLIENT_ID=$1
HOST=${2:-"localhost:8444"}  # Default to localhost:8444 if not specified

# Determine protocol (ws:// or wss://)
if [[ $HOST == *":443"* ]] || [[ $HOST == *":8444"* ]] || [[ $HOST == *".com"* ]] || [[ $HOST == *".org"* ]]; then
    PROTO="wss"
else
    PROTO="ws"
fi

echo "Connecting to ${PROTO}://${HOST}/ws/${CLIENT_ID}"

# Connect using websocat with --insecure flag for self-signed certs
websocat --insecure "${PROTO}://${HOST}/ws/${CLIENT_ID}" | while read -r line; do
    echo $line | jq '.'
done
