package operator

import (
	"context"
	"reflect"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	rbacv1 "k8s.io/api/rbac/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// headlessDatabasePodsRoleBindingSuffix is appended to the MongoDB resource name to form the name
// of the RoleBinding that grants the headless readiness probe permissions to a custom pod service
// account.
const headlessDatabasePodsRoleBindingSuffix = "-database-pods"

func headlessDatabasePodsRoleBindingName(mdb *mdbv1.MongoDB) string {
	return mdb.Name + headlessDatabasePodsRoleBindingSuffix
}

// headlessDatabasePodsSubjects returns the subject list that binds the database-pods Role to the
// pod service account of the given resource.
func headlessDatabasePodsSubjects(serviceAccountName, namespace string) []rbacv1.Subject {
	return []rbacv1.Subject{{
		Kind:      rbacv1.ServiceAccountKind,
		Name:      serviceAccountName,
		Namespace: namespace,
	}}
}

// ensureHeadlessDatabasePodsRoleBinding makes sure the pod service account of a headless MongoDB
// resource may execute the headless readiness probe. The probe reads the <name>-config Secret and
// patches the agent.mongodb.com/version annotation onto its own pod, so it needs the secrets get
// and pods patch permissions carried by the mongodb-kubernetes-database-pods Role. The Helm chart
// only binds that Role to the default pod service account, which leaves resources using a custom
// spec.podSpec.serviceAccountName without the required permissions.
//
// The RoleBinding is namespaced and owned by the MongoDB resource, so it is garbage collected
// together with the resource. It is only created for custom service accounts: when the default
// service account is used the chart-provided binding is reused, and a binding left behind by a
// previously configured custom service account is removed so the old account does not retain the
// permissions.
func ensureHeadlessDatabasePodsRoleBinding(ctx context.Context, client kubernetesClient.Client, mdb *mdbv1.MongoDB, serviceAccountName string, log *zap.SugaredLogger) error {
	name := headlessDatabasePodsRoleBindingName(mdb)
	key := kube.ObjectKey(mdb.Namespace, name)

	binding := &rbacv1.RoleBinding{}
	exists := true
	if err := client.Get(ctx, key, binding); err != nil {
		if !apiErrors.IsNotFound(err) {
			return xerrors.Errorf("failed to read RoleBinding %s: %w", key, err)
		}
		exists = false
		binding = &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: mdb.Namespace}}
	}

	if serviceAccountName == util.MongoDBServiceAccount {
		if !exists {
			return nil
		}
		if err := client.Delete(ctx, binding); err != nil && !apiErrors.IsNotFound(err) {
			return xerrors.Errorf("failed to delete stale RoleBinding %s: %w", key, err)
		}
		log.Infof("Deleted RoleBinding %s; the default pod service account is already bound by the Helm chart", key)
		return nil
	}

	expectedSubjects := headlessDatabasePodsSubjects(serviceAccountName, mdb.Namespace)
	expectedRoleRef := rbacv1.RoleRef{
		APIGroup: rbacv1.GroupName,
		Kind:     "Role",
		Name:     util.MongoDBServiceAccount,
	}

	if exists &&
		reflect.DeepEqual(binding.RoleRef, expectedRoleRef) &&
		reflect.DeepEqual(binding.Subjects, expectedSubjects) &&
		len(binding.OwnerReferences) > 0 {
		return nil
	}

	binding.OwnerReferences = kube.BaseOwnerReference(mdb)
	binding.RoleRef = expectedRoleRef
	binding.Subjects = expectedSubjects

	if !exists {
		if err := client.Create(ctx, binding); err != nil {
			return xerrors.Errorf("failed to create RoleBinding %s for service account %s: %w", key, serviceAccountName, err)
		}
		log.Infof("Created RoleBinding %s binding Role %s to service account %s for the headless readiness probe", key, util.MongoDBServiceAccount, serviceAccountName)
		return nil
	}

	if err := client.Update(ctx, binding); err != nil {
		return xerrors.Errorf("failed to update RoleBinding %s for service account %s: %w", key, serviceAccountName, err)
	}
	log.Infof("Updated RoleBinding %s binding Role %s to service account %s for the headless readiness probe", key, util.MongoDBServiceAccount, serviceAccountName)
	return nil
}
