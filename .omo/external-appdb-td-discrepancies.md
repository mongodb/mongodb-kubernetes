# External AppDB TD vs Code — Discrepancies to Fix

Review of Technical Design (https://docs.google.com/document/d/1y2nIKF0jvWuCtmEqImYm1DKvEso8LpIBUrof6Xj11YI) against `external-appdb/basic-logic` branch.

The implementation is divided into phases:
- **Phase 1**: Single-cluster, MongoDB-only. TLS/CA support is part of Phase 1 but the implementation is WIP and will ship immediately after the base stack.
- **Phase 2**: Multi-cluster support (MongoDBMultiCluster).

The TD should be updated to reflect this phasing early in the document, before describing procedures and goals.

## Blocking

### 1. Reverse Migration — No Finalizer
- **TD says**: AppDB-role CRs register `mongodb.com/appdb-detach` finalizer. Deleting the CR triggers a finalizer-gated detach before GC.
- **Code does**: No finalizer exists. `OnDelete` only skips Ops Manager cleanup. Deleting the MongoDB CR while it owns the StatefulSet triggers immediate Kubernetes GC.
- **Actual implementation**: Reverse migration is annotation-based. Safe sequence: remove `externalApplicationDatabaseRef` → OM sets `AppDBReverseMigrationReadyAnnotation` → MongoDB CR strips OwnerReference → OM reclaims → then delete CR.
- **Fix**: Update TD to document the actual annotation-based reverse migration flow and required ordering. Remove finalizer-based design. Additionally, document why resources are not cleaned up after migration: removing the project in OM would make it impossible to go back and read historical metrics. It is the user's responsibility to clean up the project. This must also be added to the Documentation section.

### 2. Multi-Cluster Support — Not Implemented
- **TD says**: "Support for both single-cluster and multi-cluster AppDB topologies from day one" (Goal #5). API enum includes `MongoDBMultiCluster`.
- **Code does**: `MongoDBMultiCluster` explicitly rejected at admission (`mongodbmulti_validation.go:58`). `MongoDB` with multi-cluster topology also rejected (`mongodb_validation.go:464`). External reconciler only handles `Kind: MongoDB`.
- **Fix**: Reclassify multi-cluster as Phase 2. Remove from current goals and API examples. Add a Phase 2 section describing planned support. Explain the phase division in an early paragraph before the goals section.

### 3. TLS/CA Management — WIP (Part of Phase 1)
- **TD says**: "Resolve MongoDB CR to extract security.tls.ca field and TLS flag and use this in the reconciler."
- **Code does**: External mode deliberately skips `mms.mongoSSL` and `mms.mongoCA` configuration. CA ConfigMap name returns empty string. Documented as TODO with "Tracked as a separate PR."
- **Fix**: TLS is part of Phase 1 scope but the implementation is WIP and will ship soon after this stack. Mark TLS as WIP in the TD. Add a section describing the current limitation and the planned fix. Do not move TLS to a later phase — it belongs in Phase 1.

### 4. PasswordSecretKeyRef and AutomationConfigOverride Not Supported
- **TD says**: "Reuse existing credentials — no copy needed."
- **Code does**: When `PasswordSecretKeyRef` is set, the internal AppDB controller deletes the operator-managed secret. The external reconciler and MongoDB CR controller can't find it → generate a new random password → silently rotate the user's password. Additionally, `AutomationConfigOverride` (available on internal AppDB) has no equivalent on the MongoDB CR.
- **Fix**: Customer-provided password secrets via `PasswordSecretKeyRef` will not be supported during forward migration. The `AppDBSpec` might be completely empty when `ExternalApplicationDatabaseRef` is set, so reading `PasswordSecretKeyRef` is unreliable. It is simpler to generate a new password and not complicate the logic. Both `PasswordSecretKeyRef` non-support and `AutomationConfigOverride` non-support should be noted in the Known Limitations section of the TD.

## Important

### 5. Annotations — Two Instead of One
- **TD says**: Single annotation `mongodb.com/appdb-migration-ready` for both directions.
- **Code does**: Two separate annotations: `mongodb.com/appdb-migration-ready` (forward) and `mongodb.com/appdb-reverse-migration-ready` (reverse). Annotations persist through ownership transfer and are cleared by STS rebuild, not at adoption.
- **Fix**: Update TD to document the two-annotation protocol and the new lifecycle (persists through transfer, cleared by STS reshape). The Excalidraw diagrams must also be updated to reflect the two-annotation protocol and the persistence-through-transfer lifecycle.

### 6. Forward Migration Scope — No ConfigMap Transfer
- **TD says**: "Strip OwnerReferences from AppDB StatefulSet, password secret, and ConfigMaps."
- **Code does**: Only the StatefulSet OwnerReferences are stripped by `requestAppDBForwardMigration`. Password and keyfile secrets are later claimed by the MongoDB CR controller. No ConfigMaps are transferred.
- **Fix**: Update TD to describe the staged handover (STS first, secrets claimed by MongoDB controller separately). State explicitly that no ConfigMaps are transferred — only the StatefulSet and secrets.

### 7. "Without Downtime" Claim Overstated
- **TD says**: "Customers should be able to adopt this incrementally on an existing Ops Manager deployment without downtime."
- **Code does**: Ownership transfer is metadata-only, but the subsequent StatefulSet rebuild reshapes the pod template (container set changes between internal-AppDB and MongoDB-CR shapes). This causes rolling pod replacement in both migration directions.
- **Fix**: Change the claim to "no data movement or StatefulSet/PVC recreation during ownership transfer." Additionally note that 1-2 minute downtime might occur if the configuration of the MongoDB/AppDB role CR differs from the internal AppDB configuration. If the configurations are semantically identical, no downtime should be present.

### 8. Fresh Start Bootstrap Circular Dependency
- **TD says**: Procedure 1 (Fresh Start) creates a MongoDB CR with `spec.role: AppDB`.
- **Code does**: The MongoDB reconciler requires an Ops Manager connection to function (project config, credentials, agent registration). A new Ops Manager can't start without AppDB, while the AppDB-role MongoDB CR can't reconcile without an available Ops Manager.
- **Fix**: Document the bootstrap architecture — fresh start requires an existing Ops Manager (Meta OM) to manage the MongoDB CR. Document the Meta OM prerequisite.

### 9. Secret Naming Discrepancy
- **TD says**: Password secret is `<om-name>-om-password`.
- **Code does**: Actual secret name is `<om-name>-db-om-password` (because `OpsManagerUserPasswordSecretName` is called with the AppDB name `<om-name>-db`, not the OM name).
- **Fix**: Correct the TD to say `<om-name>-db-om-password`.

### 10. Status/Phase Reporting Gaps
- **TD says**: No description of AppDB status semantics for external mode.
- **Code does**: Forward migration errors are written to OpsManager status (not AppDB). Successful detachment immediately sets AppDB to `Disabled` before MongoDB adoption is confirmed. Reverse migration shows two disconnected messages across two resources.
- **Fix**: Add migration-specific status conditions and document expected phases in the TD.

## Minor

### 11. Naming Convention Validation Location
- **TD says**: Validated in `validateExternalAppDBReference`.
- **Code does**: Validated in centralized `opsmanager_validation.go` via `ProcessValidationsOnReconcile`. Same result, different location.
- **Fix**: Update TD to reference the actual validation location.

### 12. Disabled Status Not Documented
- **TD says**: No description of AppDB status for external mode.
- **Code does**: External reconciler returns `workflow.Disabled()`. Continuation logic depends on exact `reconcile.Result` value equality (24h requeue matches `emptyResult` sentinel).
- **Fix**: Document the `Disabled` phase and the continuation mechanism in the TD.

### 13. Annotation Lifecycle Comments Stale
- **TD says**: Annotations cleared at adoption/reclaim.
- **Code does**: Comments in `constants.go` describe the old lifecycle (annotations removed at adoption). The code now preserves annotations through ownership transfer and clears them via STS rebuild.
- **Fix**: Update comments in `constants.go` to reflect the actual lifecycle. Update TD to match.

### 14. E2E Coverage Notes
- **TD says**: "3 scenarios must be covered with e2e tests."
- **Code does**: All 3 procedures have E2E tests. Both reverse migration variants are covered: graceful (remove reference first, delete CR after handover) and fallback (delete CR first, accept downtime and recreation). Neither variant tests the TD's finalizer-based delete-first procedure because it doesn't exist in the code.
- **Fix**: Update TD to document the two reverse migration variants and their E2E coverage.

## Recommended TD Structure

1. **Phase division**: Explain Phase 1 (single-cluster, MongoDB-only, TLS WIP) and Phase 2 (multi-cluster) in an early paragraph
2. **Phase 1 scope**: Single-cluster, MongoDB-only. TLS is part of Phase 1 but implementation is WIP
3. **Phase 1.5 (WIP)**: TLS/CA parity for external AppDB — ships immediately after base stack
4. **Phase 2**: Multi-cluster support (MongoDBMultiCluster)
5. **Reverse migration**: Replace finalizer-based design with actual annotation-based flow. Document why project cleanup is user's responsibility (metrics history, reversibility)
6. **Annotations**: Document two-annotation protocol and persistence-through-transfer lifecycle. Update Excalidraw diagrams
7. **Forward migration**: Document staged handover — STS and secrets only, no ConfigMaps
8. **Downtime claim**: Change to "no data movement or STS/PVC recreation." Note 1-2 min downtime if configs differ, no downtime if semantically identical
9. **Secret naming**: Correct to `<om-name>-db-om-password`
10. **Fresh start**: Document Meta OM bootstrap prerequisite
11. **Status reporting**: Document expected phases and messages
12. **Known Limitations**: Add `PasswordSecretKeyRef` non-support (AppDBSpec may be empty, simpler to generate new password) and `AutomationConfigOverride` non-support
13. **Documentation section**: Add project cleanup responsibility note
