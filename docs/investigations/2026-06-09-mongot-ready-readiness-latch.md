# mongot `/ready` readiness: sticky one-way latch, not a per-range initial-sync gate

- **Date:** 2026-06-09
- **mongot repo read (read-only):** `mongot` master @ `2cb4bc70bc3a3aa9821e39ac1c5c037bded93e23`
- **MCK repo references:** `mongodb-kubernetes` branch `search/lsierant/routed-from-another-shard-e2e`

All file paths below are absolute. mongot paths are rooted at `~/mdb/mongot`; MCK paths at `~/mdb/search_lsierant_routed-from-another-shard-e2e`.

---

## Question under test

> Does mongot's `/ready` readiness endpoint gate on index initial-sync state, and does a post-startup new INITIAL_SYNC range flip an already-ready pod back to not-ready?

## The claim being checked

> "mongot `/ready` does NOT gate on whether every index/range finished initial-sync."

## Verdict

The blanket claim is **TRUE in the steady/running case, FALSE for a brand-new (never-ready) server.**

Precise, correct characterization:

- `/ready` gates on *all indexes being past INITIAL_SYNC* **only for the very first readiness transition of a fresh server** (one that has never been marked ready).
- Readiness is a **sticky, one-way latch**: implemented by an in-process `volatile boolean isReady` plus a persisted `ServerStateEntry.ready` flag. Once a server reports ready, `/ready` returns 200 for the rest of the process lifetime (and across restarts while heartbeats are fresh) **without re-validating index state**.
- Therefore a NEW index, a NEW generation, or a migrated range entering INITIAL_SYNC **after** the pod is already ready does **NOT** flip `/ready` back to not-ready. The pod stays in the load-balancer rotation.
- The INITIAL_SYNC query-time rejection is a **separate** mechanism on the index object, unrelated to the `/ready` HTTP path. A pod can be `/ready`=200 and still reject a specific index's query with an INITIAL_SYNC error.

---

## Evidence (point by point)

### 1. MCK operator wires the k8s readiness probe to mongot's `/ready`

`controllers/searchcontroller/search_construction.go:40-41`

```go
SearchLivenessProbePath      = "/health"
SearchReadinessProbePath     = "/ready"
```

`controllers/searchcontroller/search_construction.go:291-297` — readiness probe is an HTTP GET to `/ready` on mongot's health-check port:

```go
func mongotReadinessProbe(search *searchv1.MongoDBSearch) func(*corev1.Probe) {
	return probes.Apply(
		probes.WithHandler(corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Scheme: corev1.URISchemeHTTP,
				Port:   intstr.FromInt32(search.GetMongotHealthCheckPort()),
				Path:   SearchReadinessProbePath,
			},
		}),
```

(The liveness probe, defined just above, hits `/health`.)

### 2. mongot route map: `/ready` -> `ReadinessCheckRequestHandler`

`src/main/java/com/xgen/mongot/server/http/HealthCheckServer.java:44-46` — a JDK `com.sun.net.httpserver.HttpServer`:

```java
Map<String, HttpHandler> handlers =
    Map.of(
        "/health", new HealthCheckRequestHandler(helper, healthManager),
        "/ready", new ReadinessCheckRequestHandler(helper, readinessChecker));
```

### 3. `/ready` handler returns 200/503 purely from `readinessChecker.isReady(...)`

`src/main/java/com/xgen/mongot/server/http/ReadinessCheckRequestHandler.java:42-46`:

```java
this.requestHelper.sendJsonResponse(
    exchange,
    this.readinessChecker.isReady(validationResponse.allowFailedIndexes)
        ? HttpRequestHelper.RESPONSES.serving()      // 200
        : HttpRequestHelper.RESPONSES.notServing()); // 503
```

The handler's own docstring (lines 11-15) claims it "waits for all indexes to be built and queryable" — but that describes only the *initial* transition (see point 5). The implementation is `CommunityReadinessChecker`.

### 4. Fresh server: all indexes must be in a valid state, with INITIAL_SYNC excluded

`src/main/java/com/xgen/mongot/config/provider/community/CommunityReadinessChecker.java:130-149` — for a never-yet-ready server, after replication/topology checks it requires all catalog indexes present and each in a valid query state:

```java
this.indexInfoProvider.refreshIndexInfos();
List<IndexInformation> indexInfos = this.indexInfoProvider.getIndexInfos();
if (!hasAllIndexesFromCatalog(indexInfos)) { /* not ready */ }
if (indexInfos.isEmpty()) { return markReady(); }
if (!areIndexesReadyToQuery(indexInfos, allowFailedIndexes)) { /* not ready */ }
return markReady();
```

`areIndexesReadyToQuery` is an `allMatch` over every index (lines 180-188). The per-index valid-state set is in `isIndexInValidState` (lines 226-230):

```java
return statusCode == IndexStatus.StatusCode.STEADY
    || statusCode == IndexStatus.StatusCode.RECOVERING_TRANSIENT
    || statusCode == IndexStatus.StatusCode.RECOVERING_NON_TRANSIENT
    || statusCode == IndexStatus.StatusCode.DOES_NOT_EXIST
    || statusCode == IndexStatus.StatusCode.STALE;
```

`INITIAL_SYNC` is a real status code (`src/main/java/com/xgen/mongot/index/status/IndexStatus.java:123`) and is **not** in that list -> an index in INITIAL_SYNC makes a fresh server report not-ready. So the first-ever readiness transition *does* gate on initial sync, for all indexes.

### 5. The sticky one-way latch (why already-ready pods never re-gate)

`src/main/java/com/xgen/mongot/config/provider/community/CommunityReadinessChecker.java:52` — in-process latch:

```java
@Var private volatile boolean isReady = false;
```

`...:72-77` — once true, short-circuit for the rest of the process lifetime, with the explicit anti-flapping rationale:

```java
// Once isReady is set to true, cache it in memory for the lifetime of the process to avoid
// the readiness state from flapping as indexes are built/deleted causing the server being
// removed from the LB.
if (this.isReady) {
  return true;
}
```

`...:152-158` — `markReady()` also persists the flag:

```java
private boolean markReady() throws MetadataServiceException {
  this.metadataService
      .getServerState()
      .updateOne(this.serverInfo.id(), ServerStateEntry.updateReadinessStatus(true));
  this.isReady = true;
  return true;
}
```

`...:117-128` — restart short-circuit: a previously-ready server skips index validation entirely:

```java
// If this server was previously marked as ready, skip the full readiness validation
// (replication init, index builds, etc.) and immediately report as ready. We only require
// indexes to be built before marking a *new* server as ready ...
// For a restarting server, the indexes were already built previously, so we don't want to
// wait for any in-progress index builds before the k8s readiness probe passes ...
if (serverStateEntry.get().ready()) {
  LOG.info("Server was already marked as ready");
  this.isReady = true;
  return true;
}
```

The persisted flag is honored while heartbeats are fresh — `ServerStateEntry.ready()` = `ready && !isReadinessStateExpired()`, with a 15-minute `READINESS_STATE_EXPIRE_TIME` (`src/main/java/com/xgen/mongot/catalogservice/ServerStateEntry.java:22,109,116-117`).

Net: after the first ready transition, neither a new index nor a new INITIAL_SYNC range can drive `/ready` back to 503 within the same process; on restart it stays ready as long as the persisted flag has not expired.

### 6. INITIAL_SYNC query rejection is independent of `/ready`

`src/main/java/com/xgen/mongot/index/lucene/LuceneSearchIndex.java:286-296` — a per-index, per-query check on the index object, with no link to the HTTP readiness path:

```java
@Override
public void throwIfUnavailableForQuerying() throws IndexUnavailableException {
  IndexStatus.StatusCode statusCode = this.getStatus().getStatusCode();
  switch (statusCode) {
    case UNKNOWN, NOT_STARTED, INITIAL_SYNC, FAILED ->
        throw new IndexUnavailableException(
            String.format(
                "cannot query search index %s while in state %s", this.definition, statusCode));
    case STEADY, STALE, RECOVERING_TRANSIENT, RECOVERING_NON_TRANSIENT, DOES_NOT_EXIST -> {
      // do nothing
    }
  }
}
```

Same pattern for vector indexes: `src/main/java/com/xgen/mongot/index/lucene/LuceneVectorIndex.java:213` and `src/main/java/com/xgen/atlas/index/vectorlite/VectorliteCompositeVectorIndex.java:160`.

Because this check lives on the index and runs at query execution, a pod can be `/ready`=200 yet still reject one specific index's query with an INITIAL_SYNC error.

---

## Operational consequence for MCK (GA-relevant availability gap)

In a sharded deployment, when a new shard is added its mongot starts with an empty data slice. With no (or only empty/steady) indexes, `CommunityReadinessChecker.isReady` latches the pod **Ready** almost immediately (`indexInfos.isEmpty()` -> `markReady()`, or all-steady -> `markReady()`), persisting `ServerStateEntry.ready=true` and setting the in-process `isReady` latch.

When the balancer subsequently migrates chunks onto that shard, the corresponding index data enters **INITIAL_SYNC**. Because of the sticky latch (point 5), `/ready` does **not** flip back to 503 — the pod remains Ready and stays in the Envoy LB rotation. The load balancer therefore **cannot route around** the still-syncing range.

Queries that land on that mongot for the migrating range hit `throwIfUnavailableForQuerying` (point 6) and either:

- fail with `cannot query search index ... while in state INITIAL_SYNC`, or
- stall until the client's `maxTimeMS` if upstream retries/waits.

This is an availability gap: readiness is a coarse, once-only "this server has finished its first build" signal, not a per-range/per-index health signal. It cannot protect a freshly-Ready shard from serving (and rejecting) queries for ranges that are still syncing after migration. Worth tracking as a GA-relevant concern for sharded search.
