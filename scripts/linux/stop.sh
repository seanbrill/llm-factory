#!/usr/bin/env bash
# Stop and remove the local-llm factory container.
# Note: model containers the factory launched run on the host daemon
# independently and are NOT stopped here — remove those from the UI.
cd "$(dirname "$0")/../.."
removed=0
for name in local-llm-factory local-llm-factory-dev; do
  if [ -n "$(docker ps -aq -f name=^${name}$)" ]; then
    docker rm -f "$name" >/dev/null && echo "removed $name" && removed=1
  fi
done
[ "$removed" = 1 ] || echo "no factory containers running."
