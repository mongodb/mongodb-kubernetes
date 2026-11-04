---
kind: feature
date: 2026-10-27
---

* **MongoDBOpsManager**: The operator can now manage the Enterprise Advanced license of Ops Manager through the new `spec.license` field.
  * `spec.license.secretRef` points at a Secret holding the signed license in its `license` key. The operator installs it in Ops Manager, and releases it when the field is removed or a newer license replaces it.
  * In air gapped deployments, the operator writes release receipts to Secrets named `<artifactId>-release-receipt`. List licenses in `spec.license.releaseReceipts`, or set `spec.license.releaseReceiptsPolicy` to `Automatic` to get a receipt for every license the operator releases.
  * The `mongodb.com/license-recompute` and `mongodb.com/license-rotate-identity` annotations ask Ops Manager to recompute its license state or rotate its license identity.
  * `status.opsManager.license` reports the licenses, entitlements, enforcement state and identity Ops Manager holds, and the `LicenseReconciled` condition reports whether the license configuration was applied.
