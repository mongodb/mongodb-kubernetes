package searchcontroller

import (
	"fmt"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1" //nolint:depguard
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
)

func NewCommunityResourceSearchSource(mdbc *mdbcv1.MongoDBCommunity) SearchSourceDBResource {
	return &CommunitySearchSource{MongoDBCommunity: mdbc}
}

type CommunitySearchSource struct {
	*mdbcv1.MongoDBCommunity
}

func (r *CommunitySearchSource) HostSeeds(shardName string) ([]string, error) {
	if shardName != "" {
		return nil, fmt.Errorf("shardName is not supported for replica set")
	}
	seeds := make([]string, r.Spec.Members)
	clusterDomain := r.Spec.GetClusterDomain()
	for i := range seeds {
		seeds[i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d", r.Name, i, r.ServiceName(), r.Namespace, clusterDomain, r.GetMongodConfiguration().GetDBPort())
	}
	return seeds, nil
}

func (r *CommunitySearchSource) KeyfileSecretName() string {
	return r.MongoDBCommunity.GetAgentKeyfileSecretNamespacedName().Name
}

func (r *CommunitySearchSource) TLSConfig() *TLSSourceConfig {
	if !r.Spec.Security.TLS.Enabled {
		return nil
	}

	var volume corev1.Volume
	watchedResources := make(map[watch.Type][]types.NamespacedName)

	if r.Spec.Security.TLS.CaCertificateSecret != nil {
		volume = statefulset.CreateVolumeFromSecret("ca", r.Spec.Security.TLS.CaCertificateSecret.Name)
		watchedResources[watch.Secret] = []types.NamespacedName{r.TLSCaCertificateSecretNamespacedName()}
	} else {
		volume = statefulset.CreateVolumeFromConfigMap("ca", r.Spec.Security.TLS.CaConfigMap.Name)
		watchedResources[watch.ConfigMap] = []types.NamespacedName{r.TLSConfigMapNamespacedName()}
	}

	return &TLSSourceConfig{
		CAFileName:       tlsCACertName,
		CAVolume:         volume,
		ResourcesToWatch: watchedResources,
	}
}

func (r *CommunitySearchSource) ResourceType() mdbv1.ResourceType {
	return mdbv1.ReplicaSet
}

func (r *CommunitySearchSource) Validate() error {
	version, err := semver.ParseTolerant(r.GetMongoDBVersion())
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", r.Spec.Version, err)
	} else if version.LT(semver.MustParse("8.2.0")) {
		return xerrors.New("MongoDB version must be 8.2.0 or higher")
	}

	return nil
}
