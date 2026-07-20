#!/bin/sh
# Entrypoint for both environments:
# - docker compose: NODE_ID/PEERS are provided explicitly.
# - Fly.io: identity is derived from the machine's process group; peers are
#   addressed via Fly private-network DNS; Raft advertises the 6PN address.
set -e

if [ -n "$FLY_APP_NAME" ]; then
  : "${NODE_ID:=$FLY_PROCESS_GROUP}"
  : "${RAFT_ADVERTISE:=[$FLY_PRIVATE_IP]:7000}"
  if [ -z "$PEERS" ]; then
    PEERS=""
    for n in node1 node2 node3; do
      host="$n.process.$FLY_APP_NAME.internal"
      PEERS="${PEERS}${PEERS:+,}$n@$host:7000@http://$host:8080"
    done
    export PEERS
  fi
  export NODE_ID RAFT_ADVERTISE
fi

exec /server
