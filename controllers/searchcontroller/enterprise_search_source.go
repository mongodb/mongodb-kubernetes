package searchcontroller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type EnterpriseResourceSearchSource struct {
	*mdbv1.MongoDB
}

func NewEnterpriseResourceSearchSource(mdb *mdbv1.MongoDB) SearchSourceDBResource {
	return EnterpriseResourceSearchSource{mdb}
}

func (r EnterpriseResourceSearchSource) HostSeeds() []string {
	seeds := make([]string, r.Spec.Members)
	clusterDomain := env.ReadOrDefault("CLUSTER_DOMAIN", "cluster.local")
	for i := range seeds {
		seeds[i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d", r.Name, i, r.ServiceName(), r.Namespace, clusterDomain, r.Spec.GetAdditionalMongodConfig().GetPortOrDefault())
	}
	return seeds
}

func (r EnterpriseResourceSearchSource) TLSConfig() *TLSSourceConfig {
	if !r.Spec.Security.IsTLSEnabled() {
		return nil
	}

	return &TLSSourceConfig{
		CAFileName: "ca-pem",
		CAVolume:   statefulset.CreateVolumeFromConfigMap("ca", r.Spec.Security.TLSConfig.CA),
		ResourcesToWatch: map[watch.Type][]types.NamespacedName{
			watch.ConfigMap: {
				{Namespace: r.Namespace, Name: r.Spec.Security.TLSConfig.CA},
			},
		},
	}
}

func (r EnterpriseResourceSearchSource) KeyfileSecretName() string {
	return fmt.Sprintf("%s-%s", r.Name, MongotKeyfileFilename)
}

func (r EnterpriseResourceSearchSource) Validate() error {
	version, err := semver.ParseTolerant(util.StripEnt(r.Spec.GetMongoDBVersion()))
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
