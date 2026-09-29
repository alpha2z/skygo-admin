# Two synthetic Skygo nodes

Build the example image with `docker build -f deploy/Dockerfile.node -t
skygo-admin/example-node:local .` from the repository root. Run two instances,
`node-a` and `node-b`, in a private Docker network. Neither contains domain data.

Each instance requires:

| Variable | Example |
|---|---|
| `NODE_ID` | `node-a` or `node-b` |
| `NODE_LISTEN` | `0.0.0.0:19001` |
| `NODE_HTTP` | `0.0.0.0:19081` |
| `NODE_SECRET_FILE` | `/run/private/cluster` |
| `NODE_REGISTRY` | `/run/config/registry.json` |

Provision a shared randomly generated secret with private file permissions. The
registry is an upstream Skygo snapshot generated from these endpoints:

```json
[{"nodeId":"node-a","address":"node-a:19001"},{"nodeId":"node-b","address":"node-b:19001"}]
```

Register each service and host with the management plane. Create a configuration
version with kind `skygo` and the endpoint array as content. An approved configure
task writes the corresponding snapshot to the inventory's `config_path`, restarts
the selected node and checks `/healthz`. `/peers` resolves the named `probe` service
on the other node using authenticated Skygo Cluster. No arbitrary method calls are
exposed by either the example or management API.

`python3 scripts/docker-smoke.py` builds and runs both examples with generated
local credentials, verifies discovery and agent operations, then removes only its
own containers and temporary files. It does not connect to existing services.
