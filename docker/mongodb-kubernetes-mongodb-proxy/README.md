### mongodb-proxy image

The proxy is PID 1 of the `mongodb` container. This image only carries the static binary at `/proxy`; it is
mounted read-only as an OCI image volume (`/mnt/proxy/proxy`) into the container that runs the official server image.

### Building locally

```bash
make mongodb-proxy                                   # bin/mongodb-proxy (linux/amd64, GOARCH=arm64 for arm64)
make mongodb-proxy-image                             # mongodb-kubernetes-mongodb-proxy:dev
make mongodb-proxy-image TARGETARCH=arm64 PROXY_IMAGE=mongodb-kubernetes-mongodb-proxy:1.0.0
```
