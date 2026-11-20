### mongodb-proxy image

The proxy is PID 1 of the `mongodb` container. This image only carries the static binary at `/proxy`; it is
mounted read-only as an OCI image volume (`/mnt/proxy/proxy`) into the container that runs the official server image.
The MongoDB server must honor `MONGODB_CONFIG_OVERRIDE_NOFORK=1`. The proxy sets it to keep the child in the foreground.

The Unix socket accepts launch requests from trusted callers. MongoDB opens and validates its own files.
Use a separate socket path for each proxy instance. On startup the proxy removes any socket at its path.

### Building locally

```bash
make mongodb-proxy-image                             # mongodb-kubernetes-mongodb-proxy:dev
make mongodb-proxy-image TARGETARCH=arm64 PROXY_IMAGE=mongodb-kubernetes-mongodb-proxy:1.0.0
```
