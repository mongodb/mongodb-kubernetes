package search_controller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type EnterpriseResourceSearchSource struct {
	*mdbv1.MongoDB
}

func NewEnterpriseResourceSearchSource(mdb *mdbv1.MongoDB) SearchSourceDBResource {
	return EnterpriseResourceSearchSource{mdb}
}

func (r EnterpriseResourceSearchSource) NamespacedName() types.NamespacedName {
	return types.NamespacedName{
		Name:      r.Name,
		Namespace: r.Namespace,
	}
}

func (r EnterpriseResourceSearchSource) Members() int {
	return r.Spec.Replicas()
}

func (r EnterpriseResourceSearchSource) GetMongoDBVersion() string {
	return r.Spec.GetMongoDBVersion()
}

func (r EnterpriseResourceSearchSource) DatabasePort() int {
	return int(r.MongoDB.Spec.GetAdditionalMongodConfig().GetPortOrDefault())
}

func (r EnterpriseResourceSearchSource) DatabaseServiceName() string {
	return r.ServiceName()
}

func (r EnterpriseResourceSearchSource) KeyfileSecretName() string {
	return fmt.Sprintf("%s-keyfile", r.Name)
}

func (r EnterpriseResourceSearchSource) IsSecurityTLSConfigEnabled() bool {
	return r.Spec.Security.IsTLSEnabled()
}

func (r EnterpriseResourceSearchSource) TLSOperatorCASecretNamespacedName() types.NamespacedName {
	return types.NamespacedName{}
}

func (r EnterpriseResourceSearchSource) Validate() error {
	version, err := semver.ParseTolerant(r.Spec.GetMongoDBVersion())
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", r.Spec.GetMongoDBVersion(), err)
	} else if version.LT(semver.MustParse("8.0.10")) {
		return xerrors.New("MongoDB version must be 8.0.10 or higher")
	}

	if r.Spec.GetTopology() != mdbv1.ClusterTopologySingleCluster {
		return xerrors.Errorf("MongoDBSearch is only supported for %s topology", mdbv1.ClusterTopologySingleCluster)
	}

	if r.GetResourceType() != mdbv1.ReplicaSet {
		return xerrors.Errorf("MongoDBSearch is only supported for %s resources", mdbv1.ReplicaSet)
	}

	authModes := r.Spec.GetSecurityAuthenticationModes()
	foundScram := false
	for _, authMode := range authModes {
		// Check for SCRAM, SCRAM-SHA-1, or SCRAM-SHA-256
		if strings.HasPrefix(strings.ToUpper(authMode), util.SCRAM) {
			foundScram = true
			break
		}
	}

	if !foundScram && len(authModes) > 0 {
		return xerrors.New("MongoDBSearch requires SCRAM authentication to be enabled")
	}

	if r.Spec.Security.GetInternalClusterAuthenticationMode() == util.X509 {
		return xerrors.New("MongoDBSearch does not support X.509 internal cluster authentication")
	}

	return nil
}
