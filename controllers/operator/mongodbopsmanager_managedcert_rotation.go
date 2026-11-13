package operator

import (
	"context"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/certs"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// managedCABundlePropagationDelay is how long the operator waits after publishing a widened CA
// bundle before re-issuing Ops Manager component leaves onto the new root. Those components read the
// bundle from a fixed path inside a mounted ConfigMap and reload it without a restart, but the
// kubelet only syncs updated ConfigMap contents after its sync period plus cache TTL.
const managedCABundlePropagationDelay = 2 * time.Minute

// reissueManagedCertificatesOnRotatedCA re-issues the Ops Manager component leaves that are still
// signed by a rotated self-signed root, once the widened bundle has had time to propagate to the
// mounted ConfigMaps. It runs after the AppDB and the Ops Manager/backup StatefulSets reached their
// goal state, so every component already trusts both roots. Errors are logged and do not fail the
// reconcile. It returns the smallest positive wait before the re-issue can be retried, or 0.
func (r *OpsManagerReconciler) reissueManagedCertificatesOnRotatedCA(ctx context.Context, opsManager *omv1.MongoDBOpsManager, log *zap.SugaredLogger) time.Duration {
	if !r.certManagerReadiness.Ready {
		return 0
	}

	var waits []time.Duration

	if opsManager.IsInternalAppDB() &&
		opsManager.Spec.AppDB != nil &&
		!opsManager.Spec.AppDB.IsMultiCluster() &&
		opsManager.Spec.AppDB.GetSecurity().IsManagedCertificateEnabled() {
		owned, err := r.appDBStatefulSetReadyForManagedCertificates(ctx, opsManager)
		if err != nil {
			log.Warnf("Skipping AppDB certificate re-issue after a CA rotation: %s", err)
		} else if owned {
			wait, err := certs.ReissueAppDBLeavesOnRotatedSelfSignedCA(ctx, r.client, opsManager,
				[]certs.Options{certs.AppDBManagedCertConfig(opsManager)}, managedCABundlePropagationDelay, log)
			if err != nil {
				log.Warnf("Failed to re-issue the AppDB certificate after a CA rotation: %s", err)
			}
			waits = append(waits, wait)
		}
	}

	if opsManager.IsManagedCertificateEnabled() {
		wait, err := certs.ReissueServerCertificateOnRotatedSelfSignedCA(ctx, r.client, opsManager,
			opsManager.Spec.Security.ManagedCertificate, certs.ServerCertificateOptions{
				NamingBase:   opsManager.Name + "-managed-om",
				SecretName:   opsManager.TLSCertificateSecretName(),
				CABundleName: opsManager.GetOpsManagerCA(),
				CABundleKey:  util.CaCertMMS,
			}, managedCABundlePropagationDelay, log)
		if err != nil {
			log.Warnf("Failed to re-issue the Ops Manager certificate after a CA rotation: %s", err)
		}
		waits = append(waits, wait)
	}

	if opsManager.Spec.Backup != nil && opsManager.Spec.Backup.Enabled && opsManager.IsBackupManagedCertificateEnabled() {
		wait, err := certs.ReissueServerCertificateOnRotatedSelfSignedCA(ctx, r.client, opsManager,
			opsManager.Spec.Backup.ManagedCertificate, certs.ServerCertificateOptions{
				NamingBase:   opsManager.Name + "-managed-backup",
				SecretName:   opsManager.QueryableBackupCertificateSecretName(),
				CABundleName: opsManager.ManagedBackupCABundleConfigMapName(),
				CABundleKey:  util.CaCertMMS,
			}, managedCABundlePropagationDelay, log)
		if err != nil {
			log.Warnf("Failed to re-issue the queryable backup certificate after a CA rotation: %s", err)
		}
		waits = append(waits, wait)
	}

	return smallestPositiveWait(waits)
}

// requeueForCertificateReissue requeues a successful reconcile after wait when a certificate
// re-issue is pending, unless the result already requeues sooner.
func requeueForCertificateReissue(result reconcile.Result, wait time.Duration) reconcile.Result {
	if wait > 0 && (result.RequeueAfter <= 0 || wait < result.RequeueAfter) {
		result.RequeueAfter = wait
	}
	return result
}

// smallestPositiveWait returns the smallest wait greater than zero, or 0 when there is none.
func smallestPositiveWait(waits []time.Duration) time.Duration {
	var smallest time.Duration
	for _, wait := range waits {
		if wait <= 0 {
			continue
		}
		if smallest == 0 || wait < smallest {
			smallest = wait
		}
	}
	return smallest
}
