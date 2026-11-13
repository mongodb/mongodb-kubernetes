package certs

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ReissueAppDBLeavesOnRotatedSelfSignedCA moves the internal AppDB's server leaf onto the AppDB's
// current self-signed root after it has rotated.
func ReissueAppDBLeavesOnRotatedSelfSignedCA(
	ctx context.Context,
	c kubernetesClient.Client,
	om *omv1.MongoDBOpsManager,
	allOpts []Options,
	minBundleAge time.Duration,
	log *zap.SugaredLogger,
) (time.Duration, error) {
	if om == nil || om.Spec.AppDB == nil || len(allOpts) == 0 || !om.Spec.AppDB.GetSecurity().IsManagedCertificateEnabled() {
		return 0, nil
	}

	res := appDBManagedCertificateOwner{om}
	if hasUserIssuer(res, categoryServer) {
		return 0, nil
	}

	return reissueServerLeafAfterBundlePropagation(ctx, c, res, allOpts, ManagedServerCABundleConfigMapName(om.AppDBName()), minBundleAge, log)
}

// ReissueServerCertificateOnRotatedSelfSignedCA moves a server-only Ops Manager component leaf (the
// Ops Manager app server or the queryable backup daemon) onto the component's current self-signed
// root after it has rotated. It applies the same bundle-propagation wait as
// ReissueAppDBLeavesOnRotatedSelfSignedCA.
func ReissueServerCertificateOnRotatedSelfSignedCA(
	ctx context.Context,
	c kubernetesClient.Client,
	owner v1.ObjectOwner,
	cfg *omv1.ManagedCertificate,
	opts ServerCertificateOptions,
	minBundleAge time.Duration,
	log *zap.SugaredLogger,
) (time.Duration, error) {
	if cfg == nil || opts.SecretName == "" {
		return 0, nil
	}

	// Built the same way EnsureServerCertificateAndCA does, so the reissue looks at the same
	// certificates and secrets.
	res := newServerCertificateOwner(owner, cfg, opts)
	if hasUserIssuer(res, categoryServer) {
		return 0, nil
	}

	allOpts := []Options{{CertSecretName: opts.SecretName}}
	return reissueServerLeafAfterBundlePropagation(ctx, c, res, allOpts, opts.CABundleName, minBundleAge, log)
}

// reissueServerLeafAfterBundlePropagation reissues the server leaf of a component once its CA
// bundle has been published for at least minBundleAge. The age is only checked when a leaf actually
// needs re-issuing, so a steady-state reconcile never waits on the bundle.
func reissueServerLeafAfterBundlePropagation(
	ctx context.Context,
	c kubernetesClient.Client,
	res CertificateOwner,
	allOpts []Options,
	bundleName string,
	minBundleAge time.Duration,
	log *zap.SugaredLogger,
) (time.Duration, error) {
	needsReissue, err := categoryLeafNeedsReissue(ctx, c, res, categoryServer, allOpts, log)
	if err != nil || !needsReissue {
		return 0, err
	}

	wait, ready, err := caBundlePropagationWait(ctx, c, res.GetNamespace(), bundleName, minBundleAge)
	if err != nil || !ready {
		return wait, err
	}

	return 0, reissueCategoryLeavesOnRotatedCA(ctx, c, res, categoryServer, allOpts, log)
}

// caBundlePropagationWait reports whether a CA bundle ConfigMap has been published for at least
// minAge, and if not, how long is left to wait. A missing publication annotation means the bundle
// was just published.
func caBundlePropagationWait(ctx context.Context, c kubernetesClient.Client, namespace, name string, minAge time.Duration) (time.Duration, bool, error) {
	cm, err := c.GetConfigMap(ctx, kube.ObjectKey(namespace, name))
	if err != nil {
		return 0, false, fmt.Errorf("could not read the CA bundle ConfigMap %s: %w", name, err)
	}

	publishedAt, ok := cm.Annotations[util.CABundlePublishedAtAnnotation]
	if !ok {
		return minAge, false, nil
	}
	published, err := time.Parse(time.RFC3339, publishedAt)
	if err != nil {
		// An unreadable timestamp is treated like a fresh publication, so the bundle is given the
		// full propagation delay.
		return minAge, false, nil
	}

	elapsed := now().UTC().Sub(published)
	if elapsed >= minAge {
		return 0, true, nil
	}
	return minAge - elapsed, false, nil
}
