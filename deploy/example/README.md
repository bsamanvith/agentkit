# Example: the agentkit image with the demo toolset

`config.json` serves the built-in `demo` toolset (`echo`, `current_time`, `calculate`) over MCP at `/mcp/demo`.

```sh
make image
export AGENTKIT_API_TOKEN=$(openssl rand -hex 16)
docker run --rm --stop-timeout 20 -p 8080:8080 -e AGENTKIT_API_TOKEN \
  -v "$PWD/deploy/example/config.json:/etc/agentkit/config.json:ro" \
  agentkit:latest
```

Then, from the host:

```sh
curl localhost:8080/readyz
curl -H "Authorization: Bearer $AGENTKIT_API_TOKEN" -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' \
  localhost:8080/mcp/demo \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"calculate","arguments":{"op":"mul","a":6,"b":7}}}'
```

The container runs as uid 65532 and needs no writable files (`--read-only` works), so the mounted config must be readable by that user (mode 0644). `docker stop` exits 0; `--stop-timeout 20` keeps Docker from killing requests still draining within the 15 s shutdown deadline. Details: [tasks/T005](../../tasks/T005-container-image.md).
