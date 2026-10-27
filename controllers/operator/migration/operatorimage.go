package migration

import (
	"context"

	"golang.org/x/xerrors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"

	mdbstatus "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	"github.com/mongodb/mongodb-kubernetes/pkg/images"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// ResolveOperatorImage returns the image used for migration dry-run Jobs, which run the
// connectivity-validator binary compiled into the operator image.
//
// The value is resolved once at startup in main.go: either from MDB_OPERATOR_IMAGE, or -- when
// that is unset, as on OLM installs -- by reading the operator's own pod via ImageFromOperatorPod.
func ResolveOperatorImage(imageUrls images.ImageUrls) (string, workflow.Status) {
	if operatorImage := imageUrls[util.OperatorImageEnv]; operatorImage != "" {
		return operatorImage, workflow.OK()
	}
	return "", operatorImageUnknown()
}

// ImageFromOperatorPod reads the image of the named container from the operator's own pod, so that
// installs which do not set MDB_OPERATOR_IMAGE work without user intervention. Callers pass the
// Downward API values (POD_NAME, NAMESPACE, OPERATOR_NAME) rather than reading the environment
// here; environment variables are read only in the main package.
//
// It returns "" for every failure mode -- missing values, Get failure, container not found -- so
// the caller can fall back to MDB_OPERATOR_IMAGE.
func ImageFromOperatorPod(ctx context.Context, reader client.Reader, namespace, podName, containerName string) string {
	if reader == nil || namespace == "" || podName == "" {
		return ""
	}

	var pod corev1.Pod
	if err := reader.Get(ctx, kube.ObjectKey(namespace, podName), &pod); err != nil {
		// Expected when running the operator outside a cluster (make run, unit tests, envtest).
		return ""
	}

	if containerName == "" && len(pod.Spec.Containers) == 1 {
		containerName = pod.Spec.Containers[0].Name
	}
	for _, c := range pod.Spec.Containers {
		if c.Name == containerName {
			return c.Image
		}
	}
	return ""
}

func operatorImageUnknown() workflow.Status {
	return workflow.Failed(xerrors.Errorf("cannot run connectivity dry-run: the operator image is unknown; set %s explicitly", util.OperatorImageEnv)).
		WithAdditionalOptions(mdbstatus.NewMigrationStatusOptionWithCondition(mdbstatus.MigrationCondition(
			mdbstatus.MigrationPhaseConnectivityCheckFailed, "OperatorImageUnknown",
			"Could not determine the operator image from the operator pod and MDB_OPERATOR_IMAGE is empty.",
		)))
}
