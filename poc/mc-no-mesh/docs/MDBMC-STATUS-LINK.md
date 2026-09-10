# Missing MongoDBMultiCluster project link

> **Historical status context:** The resources were `Running` when this defect
> was investigated. On 2026-09-08 the retained PoCs were `Pending` because
> their Cloud QA projects were inactive; `status.link` was still empty. See
> [`HANDOFF.md`](HANDOFF.md#12-current-live-state-versus-last-known-good-state).

## Finding

`MongoDBMultiCluster.status.link` is empty because the legacy
MongoDBMultiCluster status path declares the field but never assigns it. This
is independent of the resource phase: the live `lsierant/mdb` and
`lsierant2/mdb` resources are `Running`, but both omit the link.

This is not caused by a missing `projectId` in the project ConfigMap, a failed
project lookup, the dedicated operator version, or a later status update
clearing the value.

## Live evidence

The central cluster currently reports:

```text
namespace  resource  phase    status.link
lsierant   mdb       Running  <empty>
lsierant2  mdb       Running  <empty>
```

The project configuration provides the fields needed to resolve a project:

```yaml
baseUrl: https://cloud-qa.mongodb.com
orgId: 6852ed4f903a210ec0c36927
projectName: <project name>
```

The Ops Manager client resolves the project and obtains its group/project ID
from the API response. A `projectId` field in the ConfigMap is therefore not
required.

## Code path

The relevant status type is
`api/mongodb/v1/mdbmulti/mongodb_multi_types.go`:

```go
type MongoDBMultiStatus struct {
    status.Common `json:",inline"`
    // ...
    Link string `json:"link,omitempty"`
}
```

The same file's `MongoDBMultiCluster.UpdateStatus` implementation handles
common status options but does not handle `status.BaseUrlOption` and does not
write `m.Status.Link`.

The successful multicluster reconcile in
`controllers/operator/mongodbmultireplicaset_controller.go` updates status
without passing a project-link option. The common resource status writer in
`pkg/kube/commoncontroller/resourcestatus.go` only forwards the supplied
options and patches status; it does not construct a project URL itself.

In contrast, standard `MongoDB` resources already implement both pieces:

1. the reconciler supplies a link built from the connection base URL and
   project/group ID;
2. `MongoDB.UpdateStatus` handles the base URL option and assigns the status
   link.

The defect is therefore specific to `MongoDBMultiCluster`.

## Expected value

For the first PoC project, the expected URL is:

```text
https://cloud-qa.mongodb.com/v2/6a91ea0de74d992b81f33436
```

The project ID comes from the successful Cloud QA project lookup, not from the
Kubernetes ConfigMap.

## Minimal fix

The complete fix needs both changes:

1. Teach `MongoDBMultiCluster.UpdateStatus` to apply
   `status.BaseUrlOption` to `MongoDBMultiStatus.Link`.
2. In the successful MongoDBMultiCluster reconcile, pass
   `deployment.Link(conn.BaseURL(), conn.GroupID())` through the status update.

Tests should cover:

- a unit-level status update with `BaseUrlOption`;
- a successful MongoDBMultiCluster reconcile populating `status.link`;
- preservation of the link across subsequent `Running` status updates.

No live resources or source code were changed as part of this investigation.
