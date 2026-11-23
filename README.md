# guava

A server-rendered file explorer and OpenClaw executor. The executor dashboard is a separate server-rendered command.

## Development

Requires Go 1.27 or newer. Run `make fmt`, `make vet`, `make test`, or `make lint`.

## Executor dashboard

See the [executor dashboard instructions](executor/README.md#standalone-dashboard) for running the standalone dashboard command. It is separate from `fileexplorer`.

## Container images

GitHub Actions publishes two images to GHCR:

- `ghcr.io/dev1-alexwei-net/guava/fileexplorer`
- `ghcr.io/dev1-alexwei-net/guava/executor`

Each tag is a multi-platform image index for `linux/amd64` and `linux/arm64`; Docker selects the matching platform when pulling. Pushes to `main` publish `main` and `latest`. A stable `vMAJOR.MINOR.PATCH` tag publishes `vMAJOR.MINOR.PATCH`, `MAJOR.MINOR.PATCH`, `MAJOR.MINOR`, `MAJOR`, and `latest`.

Run the file explorer with its files and database persisted outside the container:

```sh
docker run --rm -p 8080:8080 \
  -v "$PWD/files:/files" \
  -v "$PWD/data:/data" \
  ghcr.io/dev1-alexwei-net/guava/fileexplorer:latest \
  -root /files -db /data/fileexplorer.sqlite
```

The executor image contains both the `executor` CLI (the default entrypoint) and `executor-dashboard`. The CLI reads the fileexplorer database and files, then contacts a reachable OpenClaw Gateway:

```sh
docker run --rm \
  --add-host host.docker.internal:host-gateway \
  -v "$PWD/data:/data:ro" \
  -v "$PWD/files:/files:ro" \
  -v "$PWD/instructions.md:/app/instructions.md:ro" \
  -e OPENCLAW_AUTH_TOKEN \
  ghcr.io/dev1-alexwei-net/guava/executor:latest \
  -database /data/fileexplorer.sqlite -root /files \
  -instructions /app/instructions.md \
  -gateway-url http://host.docker.internal:18789
```

To run the dashboard from the same image, set the dashboard binary as the entrypoint and bind it to the container interface:

```sh
docker run --rm -p 8081:8081 \
  --add-host host.docker.internal:host-gateway \
  -v "$PWD/data:/data:ro" \
  -v "$PWD/files:/files:ro" \
  -v "$PWD/instructions.md:/app/instructions.md:ro" \
  -e OPENCLAW_AUTH_TOKEN \
  --entrypoint /usr/local/bin/executor-dashboard \
  ghcr.io/dev1-alexwei-net/guava/executor:latest \
  -addr 0.0.0.0:8081 -database /data/fileexplorer.sqlite \
  -root /files -instructions /app/instructions.md \
  -gateway-url http://host.docker.internal:18789
```

The examples assume the Gateway is reachable at `host.docker.internal:18789`; change `-gateway-url` and host networking as needed. Set `OPENCLAW_AUTH_TOKEN` in the host environment when Gateway bearer authentication is enabled. The executor's `-verify-tls` flag opts into standard certificate and hostname verification.
