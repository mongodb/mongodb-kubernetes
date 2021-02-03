package om

import (
	"encoding/json"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	userv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/user"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

type AppDB struct {
	mdbv1.MongoDbSpec `json:",inline"`

	// PasswordSecretKeyRef contains a reference to the secret which contains the password
	// for the mongodb-ops-manager SCRAM-SHA user
	PasswordSecretKeyRef *userv1.SecretKeyRef `json:"passwordSecretKeyRef,omitempty"`

	// transient fields. These fields are cleaned before serialization, see 'MarshalJSON()'
	// note, that we cannot include the 'OpsManager' instance here as this creates circular dependency and problems with
	// 'DeepCopy'

	OpsManagerName string `json:"-"`
	Namespace      string `json:"-"`
}

type AppDbBuilder struct {
	appDb *AppDB
}

func DefaultAppDbBuilder() *AppDbBuilder {
	appDb := &AppDB{
		MongoDbSpec:          mdbv1.MongoDbSpec{Version: "", Members: 3, PodSpec: &mdbv1.MongoDbPodSpec{}},
		PasswordSecretKeyRef: &userv1.SecretKeyRef{},
	}
	return &AppDbBuilder{appDb: appDb}
}

func (b *AppDbBuilder) Build() *AppDB {
	return b.appDb.DeepCopy()
}

func (m AppDB) GetSecretName() string {
	return m.Name() + "-password"
}

func (m *AppDB) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *AppDB
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	// if a reference is specified without a key, we will default to "password"
	if m.PasswordSecretKeyRef != nil && m.PasswordSecretKeyRef.Key == "" {
		m.PasswordSecretKeyRef.Key = util.DefaultAppDbPasswordKey
	}

	m.ConnectionSpec.Credentials = ""
	m.ConnectionSpec.CloudManagerConfig = nil
	m.ConnectionSpec.OpsManagerConfig = nil
	m.ConnectionSpec.Project = ""
	// all resources have a pod spec
	if m.PodSpec == nil {
		m.PodSpec = mdbv1.NewMongoDbPodSpec()
	}
	return nil
}

// Name returns the name of the StatefulSet for the AppDB
func (m AppDB) Name() string {
	return m.OpsManagerName + "-db"
}

func (m AppDB) ProjectIDConfigMapName() string {
	return m.Name() + "-project-id"
}

func (m AppDB) ServiceName() string {
	if m.Service == "" {
		return m.Name() + "-svc"
	}
	return m.Service
}

func (m AppDB) AutomationConfigSecretName() string {
	return m.Name() + "-config"
}

// GetCAConfigMapName returns the name of the ConfigMap which contains
// the CA which will recognize the certificates used to connect to the AppDB
// deployment
func (a AppDB) GetCAConfigMapName() string {
	security := a.Security
	if security != nil && security.TLSConfig != nil {
		return security.TLSConfig.CA
	}
	return ""
}

// GetTlsCertificatesSecretName returns the name of the secret
// which holds the certificates used to connect to the AppDB
func (a AppDB) GetTlsCertificatesSecretName() string {
	security := a.Security
	if security != nil && security.TLSConfig != nil {
		return security.TLSConfig.SecretRef.Name
	}
	return ""
}

// ConnectionURL returns the connection url to the AppDB
func (m AppDB) ConnectionURL(userName, password string, connectionParams map[string]string) string {
	return mdbv1.BuildConnectionUrl(m.Name(), m.ServiceName(), m.Namespace, userName, password, m.MongoDbSpec, connectionParams)
}

func (m *AppDB) GetSpec() mdbv1.MongoDbSpec {
	return m.MongoDbSpec
}
func (m *AppDB) GetName() string {
	return m.Name()
}
func (m *AppDB) GetNamespace() string {
	return m.Namespace
}
