# Delve sidecar

This directory contains a Dockerfile for building a Delve sidecar container.
This sidecar is run alongside the mongodb agent container to facilitate debugging of the agent.

## Building the Delve Sidecar Image

To build the Delve sidecar image locally, use the following command:

```bash
docker buildx build --load --platform linux/amd64 . -f docker/delve-sidecar/Dockerfile -t "your-repo/delve-sidecar:latest"
```

## Usage

Once built, you can run the Delve sidecar container alongside your MongoDB agent container to enable debugging capabilities.
To do this, set the `MDB_AGENT_DEBUG` environment variable to `true` in your operator deployment.
Also set `MDB_AGENT_DEBUG_IMAGE` to the image you built above.
