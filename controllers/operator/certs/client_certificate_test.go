package certs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdbmulti"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// x509Only enables managed certificates with x509 as the only authentication mode. A single mode
// makes the agent use x509 too, so the operator creates the client CA.
func x509Only(m *mdbv1.MongoDB) {
	m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.X509}, InternalCluster: util.X509}
}

// x509ForClientsOnly enables x509 as an authentication mode, but neither internal cluster
// authentication nor the agent uses it, so the operator creates no client CA.
func x509ForClientsOnly(m *mdbv1.MongoDB) {
	m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
	m.Spec.Security.Authentication = &mdbv1.Authentication{
		Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM, util.X509}, Agents: mdbv1.AgentAuthentication{Mode: util.SCRAM},
	}
}

// appUser returns a MongoDBUser named myapp, with a client certificate, for the resource refName.
func appUser(refName string) *userv1.MongoDBUser {
	u := &userv1.MongoDBUser{Spec: userv1.MongoDBUserSpec{
		Database:           "$external",
		MongoDBResourceRef: userv1.MongoDBResourceRef{Name: refName},
		ClientCertificate:  &userv1.ClientCertificate{},
	}}
	u.Name = "myapp"
	u.Namespace = resNamespace
	return u
}

func TestClientCertificate(t *testing.T) {
	t.Run("default certificate and username", func(t *testing.T) {
		user := appUser(resName)
		clientCert, err := ClientCertificate(newMongoDB(resName, x509Only), user)
		require.NoError(t, err)

		assert.Equal(t, "myapp-client-cert", clientCert.Name)
		assert.Equal(t, resNamespace, clientCert.Namespace)
		assert.Equal(t, "myapp-cert", clientCert.Spec.SecretName)
		assert.Equal(t, []certmanagerv1.KeyUsage{certmanagerv1.UsageDigitalSignature, certmanagerv1.UsageKeyEncipherment, certmanagerv1.UsageClientAuth}, clientCert.Spec.Usages)
		assert.Empty(t, clientCert.Spec.DNSNames, "a client certificate carries no SANs")
		assert.Equal(t, "myapp", clientCert.Spec.CommonName)
		assert.Equal(t, []string{"mdb-app-client"}, clientCert.Spec.Subject.Organizations)
		assert.Equal(t, []string{resNamespace}, clientCert.Spec.Subject.OrganizationalUnits)
		// The certificate carries the subject the user is registered under.
		assert.Equal(t, "CN=myapp,OU=ns,O=mdb-app-client", user.GetUsername())
	})

	// The certificate is signed by whichever CA mongod checks client certificates against.
	signerTests := []struct {
		name       string
		mutate     func(*mdbv1.MongoDB)
		wantIssuer string
		wantKind   string
	}{
		{
			name:       "x509 internal or agent auth: operator managed client CA",
			mutate:     x509Only,
			wantIssuer: "mdb-client-ca-issuer",
			wantKind:   certmanagerv1.IssuerKind,
		},
		{
			name: "x509 internal or agent auth with a user client issuer: user configured client issuer",
			mutate: func(m *mdbv1.MongoDB) {
				x509Only(m)
				m.Spec.Security.ManagedCertificate.Client = &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "ca-issuer", Kind: certmanagerv1.ClusterIssuerKind}}
			},
			wantIssuer: "ca-issuer",
			wantKind:   certmanagerv1.ClusterIssuerKind,
		},
		{
			name: "a server issuer does not change the signer while the client CA exists",
			mutate: func(m *mdbv1.MongoDB) {
				x509Only(m)
				m.Spec.Security.ManagedCertificate.Server = &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "ca-issuer"}}
			},
			wantIssuer: "mdb-client-ca-issuer",
			wantKind:   certmanagerv1.IssuerKind,
		},
		{
			// No clusterCAFile is set, so mongod checks client certificates against the server CA.
			name:       "x509 only for clients: operator's server CA",
			mutate:     x509ForClientsOnly,
			wantIssuer: "mdb-server-ca-issuer",
			wantKind:   certmanagerv1.IssuerKind,
		},
		{
			name: "x509 only for clients with a user server issuer: user configured issuer",
			mutate: func(m *mdbv1.MongoDB) {
				x509ForClientsOnly(m)
				m.Spec.Security.ManagedCertificate.Server = &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "ca-issuer", Kind: certmanagerv1.IssuerKind}}
			},
			wantIssuer: "ca-issuer",
			wantKind:   certmanagerv1.IssuerKind,
		},
	}
	for _, tc := range signerTests {
		t.Run(tc.name, func(t *testing.T) {
			clientCert, err := ClientCertificate(newMongoDB(resName, tc.mutate), appUser(resName))
			require.NoError(t, err)
			assert.Equal(t, tc.wantIssuer, clientCert.Spec.IssuerRef.Name)
			assert.Equal(t, tc.wantKind, clientCert.Spec.IssuerRef.Kind)
		})
	}

	t.Run("the signer's duration is used in requested client cert", func(t *testing.T) {
		mdb := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			x509Only(m)
			m.Spec.Security.ManagedCertificate.Client = &mdbv1.CertificateConfig{Duration: &metav1.Duration{Duration: 30 * 24 * time.Hour}}
		})
		clientCert, err := ClientCertificate(mdb, appUser(resName))
		require.NoError(t, err)
		require.NotNil(t, clientCert.Spec.Duration)
		assert.Equal(t, 30*24*time.Hour, clientCert.Spec.Duration.Duration)
	})

	t.Run("the user's subject override flows into the certificate", func(t *testing.T) {
		user := appUser(resName)
		user.Spec.ClientCertificate.Subject = &userv1.ClientCertificateSubject{CommonName: "billing", Organizations: []string{"apps"}, OrganizationalUnits: []string{"team-a"}}
		clientCert, err := ClientCertificate(newMongoDB(resName, x509Only), user)
		require.NoError(t, err)
		assert.Equal(t, "billing", clientCert.Spec.CommonName)
		assert.Equal(t, []string{"apps"}, clientCert.Spec.Subject.Organizations)
		assert.Equal(t, []string{"team-a"}, clientCert.Spec.Subject.OrganizationalUnits)
	})

	errorTests := []struct {
		name          string
		mutate        func(*mdbv1.MongoDB)
		mutateUser    func(*userv1.MongoDBUser)
		errorContains string
	}{
		{
			name:   "subject equal to the membership subject",
			mutate: x509Only,
			mutateUser: func(u *userv1.MongoDBUser) {
				u.Spec.ClientCertificate.Subject = &userv1.ClientCertificateSubject{Organizations: []string{"mdb-server"}}
			},
			errorContains: "the client certificate subject must differ from the membership subject in organizations or organizationalUnits",
		},
		{
			name: "no operator-managed certificates",
			mutate: func(m *mdbv1.MongoDB) {
				m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.X509}}
			},
			errorContains: "does not use operator-managed certificates (spec.security.managedCertificate is not set)",
		},
		{
			// x509 internal cluster auth alone creates a client CA, but users still cannot log in
			// with a certificate unless x509 is an authentication mode.
			name: "x509 is not an authentication mode",
			mutate: func(m *mdbv1.MongoDB) {
				m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{}
				m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.SCRAM}, InternalCluster: util.X509}
			},
			errorContains: "does not use x509 authentication, so a client certificate cannot be used to log in to it",
		},
	}
	for _, tc := range errorTests {
		t.Run(tc.name, func(t *testing.T) {
			user := appUser(resName)
			if tc.mutateUser != nil {
				tc.mutateUser(user)
			}
			_, err := ClientCertificate(newMongoDB(resName, tc.mutate), user)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}

	t.Run("multi-cluster replica set", func(t *testing.T) {
		mrs := mdbmulti.DefaultMultiReplicaSetBuilder().SetName("mrs").Build()
		mrs.Namespace = resNamespace
		mrs.Spec.Security = &mdbv1.Security{
			ManagedCertificate: &mdbv1.ManagedCertificate{},
			Authentication:     &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{util.X509}, InternalCluster: util.X509},
		}
		user := appUser("mrs")
		clientCert, err := ClientCertificate(mrs, user)
		require.NoError(t, err)
		assert.Equal(t, "mrs-client-ca-issuer", clientCert.Spec.IssuerRef.Name)
		assert.Equal(t, "CN=myapp,OU=ns,O=mrs-app-client", user.GetUsername())
	})
}
