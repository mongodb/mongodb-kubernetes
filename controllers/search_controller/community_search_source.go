package search_controller

import (
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

func (r *CommunitySearchSource) Members() int {
	return r.Spec.Members
}

func (r *CommunitySearchSource) GetName() string {
	return r.Name
}

func (r *CommunitySearchSource) NamespacedName() types.NamespacedName {
	return r.MongoDBCommunity.NamespacedName()
}

func (r *CommunitySearchSource) KeyfileSecretName() string {
	return r.MongoDBCommunity.GetAgentKeyfileSecretNamespacedName().Name
}

func (r *CommunitySearchSource) GetNamespace() string {
	return r.Namespace
}

func (r *CommunitySearchSource) DatabaseServiceName() string {
	return r.ServiceName()
}

func (r *CommunitySearchSource) IsSecurityTLSConfigEnabled() bool {
	return r.Spec.Security.TLS.Enabled
}

func (r *CommunitySearchSource) DatabasePort() int {
	return r.MongoDBCommunity.GetMongodConfiguration().GetDBPort()
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
		if strings.HasPrefix(string(authMode), util.SCRAM) {
			foundScram = true
			break
		}
	}

	if !foundScram {
		return xerrors.New("MongoDBSearch requires SCRAM authentication to be enabled")
	}

	return nil
}
