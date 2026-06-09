# mongot `fromMigrate` change-stream visibility investigation

- **Date:** 2026-06-09
- **Author:** investigation against mongot source (read-only)
- **Repos / commits read:**
  - `mongot` @ `master` = `2cb4bc70bc3a3aa9821e39ac1c5c037bded93e23`
    (`CLOUDP-353553 - autoembeddings for search indexes indexing logic fixes (#6318)`, 2026-06-09)
  - All paths below are relative to the mongot repo root. Investigation was read-only; no files were modified.

## Hypothesis under test (verbatim)

> When MongoDB chunk migration moves documents onto a shard, those documents are written on the
> recipient with `fromMigrate=true` in the oplog. mongot replicates each shard's collection data in
> two phases: (1) an initial sync that does a full collection SCAN, and (2) a steady-state phase that
> tails a CHANGE STREAM for incremental updates. Change streams exclude `fromMigrate` operations by
> default. Therefore, if a new shard's mongot has already finished its initial sync (when the shard
> owned ~0 documents of the collection) and is in steady-state change-stream mode, then documents
> that later arrive via chunk migration are INVISIBLE to mongot's change stream and never get
> indexed — so a `$search` fanned out to that shard returns incomplete results or blocks.

## Verdict: REFUTED

mongot's steady-state change stream explicitly sets `showMigrationEvents(true)`, so `fromMigrate`
chunk-migration documents ARE delivered to the change stream and get indexed. The hypothesis's own
crux ("change streams exclude fromMigrate by default" → mongot is blind) fails because mongot
overrides that default on every change stream it opens.

## Evidence

### (1) Steady-state replication uses a tailing change stream — TRUE

The steady-state package drives sync through a `$changeStream` aggregate built by
`ChangeStreamAggregateOperationFactory`.

- `src/main/java/com/xgen/mongot/replication/mongodb/steadystate/changestream/ChangeStreamMongoClientFactory.java:140`
  ```java
        new ChangeStreamAggregateOperationFactory(
  ```
- `src/main/java/com/xgen/mongot/replication/mongodb/steadystate/changestream/ModeAwareChangeStreamClient.java`
  holds and uses that `ChangeStreamAggregateOperationFactory` (field + constructor param) to open the
  steady-state stream.

The stream is established with `batchSize:0` then tailed via `getMore` (resume-token based),
documented in the factory javadoc.

### (2) The change stream sets `showMigrationEvents(true)` — TRUE (the crux)

Both change-stream aggregate factories opt IN to migration events on every stream:

- `src/main/java/com/xgen/mongot/replication/mongodb/common/ChangeStreamAggregateOperationFactory.java:62`
  (steady-state path)
  ```java
            .showMigrationEvents(true)
  ```
- `src/main/java/com/xgen/mongot/replication/mongodb/common/ChangeStreamAggregateCommandFactory.java:67`
  ```java
            .showMigrationEvents(true)
  ```

The flag is serialized into the actual `$changeStream` pipeline stage:

- `src/main/java/com/xgen/mongot/replication/mongodb/common/ChangeStreamAggregateOperationBuilder.java:190-191`
  ```java
    public void showMigrationEvents(Boolean value) {
      this.pipeline.put("showMigrationEvents", new BsonBoolean(value));
  ```
- `src/main/java/com/xgen/mongot/util/mongodb/serialization/ChangeStreamPipelineStageOptionsProxy.java:75`
  ```java
            doc.append(SHOW_MIGRATION_EVENTS_FIELD, new BsonBoolean(showMigrationEvents)));
  ```

Intent is documented explicitly:

- `src/main/java/com/xgen/mongot/util/mongodb/BatchMongoClient.java:35`
  ```java
   * shard migrations with `showMigrationEvents` option on change-stream pipeline.
  ```

Guarded by an integration test:
`src/test/integration/java/com/xgen/mongot/replication/mongodb/initialsync/TestChangeStreamMetadataExclusionIntegration.java`.

### (3) Initial sync is a resumable collection scan — TRUE

- `src/main/java/com/xgen/mongot/replication/mongodb/initialsync/BufferlessCollectionScanner.java:44-45`
  ```java
  /** Responsible for scanning a collection during a bufferless initial sync. */
  public class BufferlessCollectionScanner {
  ```
- `...BufferlessCollectionScanner.java:112-113`
  ```java
    Result scanWithTimeLimit(Duration collectionScanTime) throws InitialSyncException {
      this.logger.info("Starting a collection scan phase.");
  ```

The scan uses `CollectionScanAggregateCommand` / `CollectionScanMongoClient` over the collection, so
it sees physically-present documents (including orphaned/migrated ones). This phase is not the one
relevant to the hypothesis, because steady-state already covers migration events — but it confirms
the "phase (1) is a SCAN" premise.

### (4) No separate chunk-ownership resync mechanism — and none is needed

A search of the replication path for `shardVersion`, `orphan`, `rangeDeleter`, chunk-metadata
listeners, or periodic ownership-driven resync found **no** such mechanism. (The few unrelated
matches for those terms live in metrics / leasing / batch-size code, not in the replication ingest
path.)

None is required: because the steady-state change stream sets `showMigrationEvents(true)` (point 2),
`fromMigrate=true` writes arriving via chunk migration flow through the change stream and are indexed
incrementally. mongot connects per-shard and does not need to track config-server chunk ownership to
stay correct under rebalancing.

## Why the original `$search` timeout actually happened (NOT `fromMigrate`)

The observed `$search` timeout on the newly-targeted shard was **not** caused by migrated documents
being invisible to the change stream — point (2) rules that out. The real cause was a stale-index
initial-sync loop triggered by devcontainer environment pollution, compounded by mongot readiness
latching (mongot reporting not-ready / a `$search` blocking until the per-range initial sync for that
index completes). See the companion readiness investigation report for that chain. In short: the
shard's mongot was stuck/looping in initial sync against a stale index, so `$search` blocked — a
readiness/initial-sync problem, orthogonal to `fromMigrate` semantics.

## Implications for MCK search (sharded rebalancing + mongot)

- Chunk-migrated data **is** indexed by mongot: migration writes (`fromMigrate=true`) are delivered
  to the steady-state change stream because mongot sets `showMigrationEvents(true)`.
- A shard whose mongot finished initial sync while owning ~0 docs will still pick up later-migrated
  documents via the change stream; there is no permanent blind spot from chunk balancing.
- The window to watch in sharded-rebalancing e2e is **initial-sync completion / readiness latching**
  for each index range, not change-stream migration visibility. `$search` can block until the
  relevant per-range initial sync completes and mongot reports ready, but it will not silently return
  permanently-incomplete results due to `fromMigrate`.
