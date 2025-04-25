package commoncontroller

import (
	"context"
	"encoding/json"
	"strings"

	"go.uber.org/zap"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	apiErrors "k8s.io/apimachinery/pkg/api/errors"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/10gen/ops-manager-kubernetes/mongodb-community-operator/pkg/kube/client"
)

// updateStatus updates the status for the CR using patch operation. Note, that the resource status is mutated and
// it's important to pass resource by pointer to all methods which invoke current 'updateStatus'.
func UpdateStatus(ctx context.Context, kubeClient kubernetesClient.Client, reconciledResource v1.CustomResourceReadWriter, st workflow.Status, log *zap.SugaredLogger, statusOptions ...status.Option) (reconcile.Result, error) {
	mergedOptions := append(statusOptions, st.StatusOptions()...)
	log.Debugf("Updating status: phase=%v, options=%+v", st.Phase(), mergedOptions)
	reconciledResource.UpdateStatus(st.Phase(), mergedOptions...)
	if err := patchUpdateStatus(ctx, kubeClient, reconciledResource, statusOptions...); err != nil {
		log.Errorf("Error updating status to %s: %s", st.Phase(), err)
		return reconcile.Result{}, err
	}
	return st.ReconcileResult()
}

type emptyPayload struct{}

type patchValue struct {
	Op    string      `json:"op"`
	Path  string      `json:"path"`
	Value interface{} `json:"value"`
}

// We fetch a fresh version in case any modifications have been made.
// Note, that this method enforces update ONLY to the status, so the reconciliation events happening because of this
// can be filtered out by 'controller.shouldReconcile'
// The "jsonPatch" merge allows to update only status field
func patchUpdateStatus(ctx context.Context, kubeClient kubernetesClient.Client, resource v1.CustomResourceReadWriter, options ...status.Option) error {
	payload := []patchValue{{
		Op:   "replace",
		Path: resource.GetStatusPath(options...),
		// in most cases this will be "/status", but for each of the different Ops Manager components
		// this will be different
		Value: resource.GetStatus(options...),
	}}

	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	patch := client.RawPatch(types.JSONPatchType, data)
	err = kubeClient.Status().Patch(ctx, resource, patch)

	if err != nil && apiErrors.IsInvalid(err) {
		zap.S().Debug("The Status subresource might not exist yet, creating empty subresource")
		if err := ensureStatusSubresourceExists(ctx, kubeClient, resource, options...); err != nil {
			zap.S().Debug("Error from ensuring status subresource: %s", err)
			return err
		}
		return kubeClient.Status().Patch(ctx, resource, patch)
	}

	return nil
}

// ensureStatusSubresourceExists ensures that the status subresource section we are trying to write to exists.
// if we just try and patch the full path directly, the subresource sections are not recursively created, so
// we need to ensure that the actual object we're trying to write to exists, otherwise we will get errors.
func ensureStatusSubresourceExists(ctx context.Context, kubeClient kubernetesClient.Client, resource v1.CustomResourceReadWriter, options ...status.Option) error {
	fullPath := resource.GetStatusPath(options...)
	parts := strings.Split(fullPath, "/")

	if strings.HasPrefix(fullPath, "/") {
		parts = parts[1:]
	}

	var path []string
	for _, part := range parts {
		pathStr := "/" + strings.Join(path, "/")
		path = append(path, part)
		emptyPatchPayload := []patchValue{{
			Op:    "add",
			Path:  pathStr,
			Value: emptyPayload{},
		}}
		data, err := json.Marshal(emptyPatchPayload)
		if err != nil {
			return err
		}
		patch := client.RawPatch(types.JSONPatchType, data)
		if err := kubeClient.Status().Patch(ctx, resource, patch); err != nil && !apiErrors.IsInvalid(err) {
			return err
		}
	}
	return nil
}
