# SOCKS5 Proxy Support for Local Development

## Why

When running the operator and e2e tests **on the host** (outside the Kubernetes cluster), network traffic to in-cluster services (K8s API, Ops Manager, MongoDB pods) must be tunneled. Historically this required per-cluster `ssh -L` port forwards. SOCKS5 proxy support replaces that with a single `ssh -D` dynamic tunnel (or kube-forwarding-proxy), configured once.

## Architecture

```
Host machine                          Remote bastion / jump host
┌──────────────────────┐              ┌──────────────────────┐
│ operator (go run)    │──K8S_FWD──►  │                      │
│ pytest (e2e tests)   │──PROXY────►  │  ssh -D 1080         │──► K8s API servers
│                      │──SOCKS────►  │                      │──► MongoDB pods
│                      │              │                      │──► Ops Manager
└──────────────────────┘              └──────────────────────┘
```

Two proxy mechanisms, separated by concern:

| Traffic | Mechanism | Configuration |
|---|---|---|
| **Go operator → K8s API** | Native `proxy-url` in kubeconfig | No code change (client-go reads it) |
| **Go operator → Ops Manager, telemetry** | `http.DefaultTransport.DialContext` patch | `K8S_FWD_PROXY_SOCKS` env var |
| **Python tests → everything** | Global `socket.socket` replacement | `K8S_FWD_PROXY_SOCKS` env var |

## Configuration

### 1. SSH tunnel

```bash
autossh -M 0 -N -o "ServerAliveInterval=30" -o "ServerAliveCountMax=3" \
  -D 127.0.0.1:1080 ubuntu@bastion-host
```

### 2. Kubeconfig (`proxy-url`) — for Go client-go

```yaml
clusters:
- cluster:
    server: https://api-server:6443
    proxy-url: socks5://127.0.0.1:1080
    certificate-authority-data: ...
  name: my-cluster
```

Go's client-go reads this natively. The Python `kubernetes` client ignores it (all versions through 35.0.0), which is why the Python side uses `K8S_FWD_PROXY_SOCKS` instead.

### 3. Environment variable — for Python tests and Go non-K8s HTTP

```bash
export K8S_FWD_PROXY_SOCKS=socks5://127.0.0.1:1080
```

## Python e2e test proxy (`conftest.py`)

### How it works

When `K8S_FWD_PROXY_SOCKS` is set, two global patches are applied at module load time:

**1. `socket.socket` → `socks.socksocket`**

PySocks' `socksocket` is a drop-in replacement for Python's `socket.socket`. Every library that opens a TCP connection — urllib3 (K8s REST API), websocket-client (exec/port-forward), pymongo (MongoDB wire protocol) — transparently goes through the SOCKS5 proxy.

`rdns=True` ensures DNS resolution happens at the proxy, not locally. This is critical for cluster-internal names like `*.svc.cluster.local`.

**2. `socket.getaddrinfo` fallback**

Some libraries (notably pymongo) call `getaddrinfo()` before `connect()`. For cluster-internal hostnames that don't exist in local DNS, this raises `gaierror`. The patch catches the error and returns a dummy `AF_INET` result, allowing the connection to proceed to `socket.connect()` where PySocks resolves the name remotely via the SOCKS5 proxy.

### Why global socket patching?

We evaluated three approaches:

1. **Per-library patches** (SOCKSProxyManager for urllib3, proxy_type for websocket-client, proxyHost for pymongo): Required 3 separate patches, extra dependencies (`urllib3[socks]`, `python-socks`), and pymongo doesn't even support proxy parameters.

2. **`Configuration.proxy` on the k8s client**: The Python k8s client has a `proxy` field but (a) it ignores `proxy-url` from kubeconfig, (b) `urllib3.ProxyManager` doesn't support `socks5://`, and (c) websocket-client needs a separate `proxy_type` parameter.

3. **Global `socket.socket` replacement** (chosen): One patch covers all libraries. Only requires `PySocks`. Simple, minimal code.

### Safety

The global socket replacement is safe because:
- It only activates when `K8S_FWD_PROXY_SOCKS` is set (never in CI/production)
- PySocks' `socksocket` is API-compatible with `socket.socket`
- Local addresses (127.0.0.1, etc.) work fine — the SOCKS5 proxy dials them normally
- The `getaddrinfo` fallback only triggers on `gaierror` — locally resolvable names use the original implementation

### Required package

```
PySocks>=1.7.1
```

## Go operator proxy (`main.go`)

### `configureSocksProxy()`

Patches `http.DefaultTransport.DialContext` with a SOCKS5 dialer when `K8S_FWD_PROXY_SOCKS` is set. This covers the Ops Manager retryable HTTP client and telemetry client, which derive from `DefaultTransport`.

**Does NOT affect K8s API calls** — client-go creates its own transport per `rest.Config` and reads `proxy-url` from kubeconfig natively.

**Gated on `OPERATOR_ENV != prod`** — never active in production.

### Required import

```go
"golang.org/x/net/proxy"  // already in go.mod as indirect dependency
```

## Debugging

| Symptom | Likely cause |
|---|---|
| `no such host` from pymongo | `getaddrinfo` patch not active — check `K8S_FWD_PROXY_SOCKS` is set |
| `ProxySchemeUnknown: socks5` | Old per-library patch still active, or `socket.socket` not replaced |
| `Connection to remote host was lost` (websocket) | SOCKS5 proxy not reachable, or SSH tunnel down |
| Go operator `dial tcp: lookup ... no such host` | `K8S_FWD_PROXY_SOCKS` not set, or missing `socks5://` prefix |
| K8s API calls fail but pymongo works | kubeconfig missing `proxy-url` (Go) or `K8S_FWD_PROXY_SOCKS` not set (Python) |
