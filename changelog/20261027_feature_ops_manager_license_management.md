---
kind: feature
date: 2026-10-27
---

* **MongoDBOpsManager**: The operator can now manage the Enterprise Advanced license of Ops Manager through the new `spec.license` field.
  * `spec.license.secretRef` points at a Secret holding the signed license in its `license` key. The operator installs it in Ops Manager, and releases it when the field is removed or a newer license replaces it. The operator identifies the license it installed by this Secret, so keep the Secret until the license is released.
  * Deleting the `MongoDBOpsManager` resource releases the license the operator installed, holding the resource with a finalizer until the release succeeds.
  * In air gapped deployments, the operator writes release receipts to Secrets named `<artifactId>-release-receipt`. List the Secrets of released licenses in `spec.license.releaseReceipts`. `spec.license.releaseReceiptsPolicy` defaults to `OnlyListed`, which covers only those licenses. Set it to `Automatic` to get a receipt for every license the operator releases.
  * The `mongodb.com/license-recompute` and `mongodb.com/license-rotate-identity` annotations ask Ops Manager to recompute its license state or rotate its license identity.
  * The `LicenseReady` condition reports the license state, the license Ops Manager holds and its holder ID.
  * The `LicenseManaged` condition reports whether the operator manages the license from `.spec.license.secretRef`, and the reason when it does not.
