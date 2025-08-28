package search_controller

import (
	"fmt"
	"strings"

	"github.com/blang/semver"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"

	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

func NewCommunityResourceSearchSource(mdbc *mdbcv1.MongoDBCommunity) SearchSourceDBResource {
	return &CommunitySearchSource{MongoDBCommunity: mdbc}
}

type CommunitySearchSource struct {
	*mdbcv1.MongoDBCommunity
}

func (r *CommunitySearchSource) HostSeeds() []string {
	seeds := make([]string, r.Spec.Members)
	for i := range seeds {
		seeds[i] = fmt.Sprintf("%s-%d.%s.%s.svc.cluster.local:%d", r.Name, i, r.ServiceName(), r.Namespace, r.GetMongodConfiguration().GetDBPort())
	}
	return seeds
}

func (r *CommunitySearchSource) KeyfileSecretName() string {
	return r.MongoDBCommunity.GetAgentKeyfileSecretNamespacedName().Name
}

func (r *CommunitySearchSource) IsSecurityTLSConfigEnabled() bool {
	return r.Spec.Security.TLS.Enabled
}

func (r *CommunitySearchSource) TLSOperatorCASecretNamespacedName() types.NamespacedName {
	return r.MongoDBCommunity.TLSOperatorCASecretNamespacedName()
}

func (r *CommunitySearchSource) Validate() error {
	version, err := semver.ParseTolerant(r.GetMongoDBVersion())
	if err != nil {
		return xerrors.Errorf("error parsing MongoDB version '%s': %w", r.Spec.Version, err)
	} else if version.LT(semver.MustParse("8.0.10")) {
		return xerrors.New("MongoDB version must be 8.0.10 or higher")
	}

	foundScram := false
	for _, authMode := range r.Spec.Security.Authentication.Modes {
		// Check for SCRAM, SCRAM-SHA-1, or SCRAM-SHA-256
		if strings.HasPrefix(strings.ToUpper(string(authMode)), util.SCRAM) {
			foundScram = true
			break
		}
	}

	if !foundScram && len(r.Spec.Security.Authentication.Modes) > 0 {
		return xerrors.New("MongoDBSearch requires SCRAM authentication to be enabled")
	}

	return nil
}
