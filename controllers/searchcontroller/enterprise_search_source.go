package searchcontroller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/watch"
	"github.com/mongodb/mongodb-kubernetes/pkg/statefulset"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

type EnterpriseResourceSearchSource struct {
	*mdbv1.MongoDB
	processToHostnameMap map[string]om.HostnameAndPort
}

func NewEnterpriseResourceSearchSource(mdb *mdbv1.MongoDB, processToHostnameMap map[string]om.HostnameAndPort) SearchSourceDBResource {
	return EnterpriseResourceSearchSource{MongoDB: mdb, processToHostnameMap: processToHostnameMap}
}

func (r EnterpriseResourceSearchSource) HostSeeds() []string {
	externalMembersCount := len(r.Spec.ExternalMembers)
	seeds := make([]string, r.Spec.Members+externalMembersCount)

	// populate seed list with any external members first
	// Validate() will have already checked that all external members have corresponding hostnames
	for idx, memberName := range r.Spec.ExternalMembers {
		seeds[idx] = fmt.Sprintf("%s:%d", r.processToHostnameMap[memberName].Hostname, r.processToHostnameMap[memberName].Port)
	}

	// add internal members to the end of the seed list
	clusterDomain := r.Spec.GetClusterDomain()
	for i := 0; i < r.Spec.Members; i++ {
		seeds[externalMembersCount+i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:%d", r.Name, i, r.ServiceName(), r.Namespace, clusterDomain, r.Spec.GetAdditionalMongodConfig().GetPortOrDefault())
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
	} else if version.LT(semver.MustParse("8.2.0")) {
		return xerrors.New("MongoDB version must be 8.2.0 or higher")
	}

	if r.Spec.GetTopology() != mdbv1.ClusterTopologySingleCluster {
		return xerrors.Errorf("MongoDBSearch is only supported for %s topology", mdbv1.ClusterTopologySingleCluster)
	}

	if r.GetResourceType() != mdbv1.ReplicaSet {
		return xerrors.Errorf("MongoDBSearch is only supported for %s resources", mdbv1.ReplicaSet)
	}

	// processToHostnameMap will only be set by the Search reconciler.
	// The ReplicaSet reconciler always sets it to nil, because we don't care to validate the external members mapping there.
	if r.processToHostnameMap != nil {
		for _, member := range r.Spec.ExternalMembers {
			if _, ok := r.processToHostnameMap[member]; !ok {
				return xerrors.Errorf("external member '%s' does not have a corresponding hostname in the automation config", member)
			}
		}
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

	return nil
}
