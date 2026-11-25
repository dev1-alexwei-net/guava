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

Each tag is a multi-platform image index for `linux/amd64` and `linux/arm64`; Docker selects the matching platform when pulling. Pushes to `main` publish the next automatic patch release: both images carry that version tag (for example `1.0.3`) in addition to `main` and `latest`, and the workflow then creates the matching `v1.0.3` git tag on the pushed commit. The next patch version is one above the highest existing `vMAJOR.MINOR.PATCH` git tag, seeded at `0.1.0` until the first release tag exists. Major and minor releases are cut deliberately with the manual escape hatch described in [Releasing](#releasing).

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
  -gateway-url http://host.docker.internal:18789 \
  -fileexplorer-url http://fileexplorer:8080
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
  -gateway-url http://host.docker.internal:18789 \
  -fileexplorer-url http://fileexplorer:8080
```

The examples assume the Gateway is reachable at `host.docker.internal:18789` and the fileexplorer service is reachable as `http://fileexplorer:8080`; change these URLs and host/container networking as needed. The executor refreshes fileexplorer between file routines so newly created files can be discovered during the same run. Set `OPENCLAW_AUTH_TOKEN` in the host environment when Gateway bearer authentication is enabled. The executor's `-verify-tls` flag opts into standard certificate and hostname verification.

## Releasing

There are two ways to publish a numbered release, both through the **Publish container images** workflow:

### Automatic patch (every push to main)

Each push to `main` publishes the next patch version — one above the highest existing `vMAJOR.MINOR.PATCH` git tag, seeded at `0.1.0` until the first release tag exists — for both images, alongside `main` and `latest`, then cuts the matching `v…` git tag. This is the routine path; no action is needed.

### Manual bump (major or minor escape hatch)

Major and minor releases are cut deliberately, by hand:

1. Open the repository's **Actions** tab, select **Publish container images**, and click **Run workflow**.
2. Keep the default branch (`main`) and choose the **bump** input:
   - **minor** — publishes `MAJOR.(MINOR+1).0` for a feature release with no breaking changes.
   - **major** — publishes `(MAJOR+1).0.0` for a breaking release.
3. The workflow publishes both images with that exact version tag plus `main` and `latest`, then creates and pushes the matching `v…` git tag. For example, bumping **minor** from the most recent `v1.2.7` tag publishes `1.3.0` and tags `v1.3.0`.

The manual bump derives its version from the most recent `vMAJOR.MINOR.PATCH` git tag, and the tag it creates becomes the base for the next automatic patch: after a manual **minor** bump to `1.3.0`, the next push to `main` publishes `1.3.1`. Use the hatch deliberately — it publishes to the public registry and moves the version forward.
