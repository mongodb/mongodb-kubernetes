---
kind: feature
date: 2026-10-27
---

* **MongoDBOpsManager**: The operator can now manage the Enterprise Advanced license of Ops Manager through the new `spec.license` field.
  * `spec.license.licenseSecretRef` points at a Secret holding the signed license in its `license` key. The operator installs it in Ops Manager, and releases it when the field is removed or a newer license replaces it.
  * In air gapped deployments, `spec.license.releaseReceipts` and `spec.license.autoReleaseReceipts` make the operator write the release receipt of a released license to a Secret named `<artifactId>-release-receipt`.
  * The `mongodb.com/license-recompute` and `mongodb.com/license-rotate-identity` annotations ask Ops Manager to recompute its license state or rotate its license identity.
  * `status.opsManager.license` reports the licenses, entitlements, enforcement state and identity Ops Manager holds, and the `LicenseReconciled` condition reports whether the license configuration was applied.
* **MongoDB**, **MongoDBMultiCluster**: When Ops Manager rejects a change because of license enforcement, the resource reports it through the `OpsManagerLicenseReady` condition with the `LicenseEnforced` reason. The condition clears once Ops Manager accepts changes again.
