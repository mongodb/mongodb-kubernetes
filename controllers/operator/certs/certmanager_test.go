package certs

import (
	"context"
	"fmt"
	"strings"
	"testing"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	resName      = "mdb"
	resNamespace = "ns"
)

func newMongoDB(name string, mutate func(*mdbv1.MongoDB)) *mdbv1.MongoDB {
	mdb := mdbv1.NewReplicaSetBuilder().SetName(name).SetNamespace(resNamespace).Build()
	if mutate != nil {
		mutate(mdb)
	}
	return mdb
}

func TestMergeSubject(t *testing.T) {
	def := &certmanagerv1.X509Subject{
		Organizations:       []string{"def-o"},
		OrganizationalUnits: []string{"def-ou"},
	}
	tests := []struct {
		name     string
		override *mdbv1.X509Subject
		defCN    string
		expSub   *certmanagerv1.X509Subject
		expCN    string
	}{
		{
			name:     "nil override returns the default and defCN unchanged",
			override: nil,
			defCN:    "def-cn",
			expSub:   def,
			expCN:    "def-cn",
		},
		{
			name:     "override Organization only keeps the default OU",
			override: &mdbv1.X509Subject{Organizations: []string{"my-o"}},
			defCN:    "def-cn",
			expSub:   &certmanagerv1.X509Subject{Organizations: []string{"my-o"}, OrganizationalUnits: []string{"def-ou"}},
			expCN:    "def-cn",
		},
		{
			name:     "override adds Country and keeps O/OU defaults",
			override: &mdbv1.X509Subject{Countries: []string{"US"}},
			expSub:   &certmanagerv1.X509Subject{Organizations: []string{"def-o"}, OrganizationalUnits: []string{"def-ou"}, Countries: []string{"US"}},
			expCN:    "",
		},
		{
			name:     "override CommonName wins over defCN",
			override: &mdbv1.X509Subject{CommonName: "my-cn"},
			defCN:    "def-cn",
			expSub:   &certmanagerv1.X509Subject{Organizations: []string{"def-o"}, OrganizationalUnits: []string{"def-ou"}},
			expCN:    "my-cn",
		},
		{
			name:     "empty override CommonName falls back to defCN",
			override: &mdbv1.X509Subject{Organizations: []string{"my-o"}},
			defCN:    "def-cn",
			expSub:   &certmanagerv1.X509Subject{Organizations: []string{"my-o"}, OrganizationalUnits: []string{"def-ou"}},
			expCN:    "def-cn",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSub, gotCN := mergeSubject(tc.override, def, tc.defCN)
			assert.Equal(t, tc.expSub, gotSub)
			assert.Equal(t, tc.expCN, gotCN)
		})
	}
}

// TestSignerIssuerRef covers the per-category signer resolution: the user-configured issuer
// when present, otherwise the operator's self-signed CA issuer. Also exercises hasUserIssuer.
func TestSignerIssuerRef(t *testing.T) {
	tests := []struct {
		name           string
		mc             *mdbv1.ManagedCertificate
		category       certCategory
		categoryIssuer cmmeta.ObjectReference
	}{
		{
			name:           "server with a user issuer passes it through",
			mc:             &mdbv1.ManagedCertificate{Enabled: true, Server: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "my-issuer", Kind: "ClusterIssuer"}}},
			category:       categoryServer,
			categoryIssuer: cmmeta.ObjectReference{Name: "my-issuer", Kind: "ClusterIssuer"},
		},
		{
			name:           "server without an issuer uses the self-signed CA issuer",
			mc:             &mdbv1.ManagedCertificate{Enabled: true},
			category:       categoryServer,
			categoryIssuer: cmmeta.ObjectReference{Name: fmt.Sprintf("%s-server-ca-issuer", resName), Kind: certmanagerv1.IssuerKind},
		},
		{
			name:           "client with a user issuer passes it through",
			mc:             &mdbv1.ManagedCertificate{Enabled: true, Client: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: "client-issuer", Kind: "Issuer"}}},
			category:       categoryClient,
			categoryIssuer: cmmeta.ObjectReference{Name: "client-issuer", Kind: "Issuer"},
		},
		{
			name:           "client without an issuer uses the self-signed CA issuer",
			mc:             &mdbv1.ManagedCertificate{Enabled: true},
			category:       categoryClient,
			categoryIssuer: cmmeta.ObjectReference{Name: fmt.Sprintf("%s-client-ca-issuer", resName), Kind: certmanagerv1.IssuerKind},
		},
		{
			name:           "an empty issuer name is treated as no issuer (self-signed)",
			mc:             &mdbv1.ManagedCertificate{Enabled: true, Server: &mdbv1.CertificateConfig{IssuerRef: &mdbv1.IssuerRef{Name: ""}}},
			category:       categoryServer,
			categoryIssuer: cmmeta.ObjectReference{Name: fmt.Sprintf("%s-server-ca-issuer", resName), Kind: certmanagerv1.IssuerKind},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner := newMongoDB(resName, func(m *mdbv1.MongoDB) { m.Spec.Security.ManagedCertificate = tc.mc })
			assert.Equal(t, tc.categoryIssuer, signerIssuerRef(owner, tc.category))
		})
	}
}

// TestVerifyAgentSubjectDistinct covers the guard that rejects an agent subject matching the
// membership subject on both O and OU (which would make mongod treat the agent as a peer).
func TestVerifyAgentSubjectDistinct(t *testing.T) {
	auth := func(internalX509, agentX509 bool) *mdbv1.Authentication {
		a := &mdbv1.Authentication{Enabled: true}
		if internalX509 {
			a.InternalCluster = util.X509
		}
		if agentX509 {
			a.Modes = []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}
		}
		return a
	}
	tests := []struct {
		name  string
		auth  *mdbv1.Authentication
		subs  *mdbv1.CertSubjects
		expOK bool
	}{
		{name: "internal auth not x509 is a no-op", auth: auth(false, true), expOK: true},
		{name: "agent auth not x509 is a no-op", auth: auth(true, false), expOK: true},
		{
			name:  "internal and agent auth are x509 with default subjects (created by operator) differ in O",
			auth:  auth(true, true),
			expOK: true,
		},
		{
			name: "internal and agent auth x509 with the same O and OU (defaults to same between member and agent) is rejected",
			auth: auth(true, true),
			subs: &mdbv1.CertSubjects{
				Membership: &mdbv1.X509Subject{Organizations: []string{"same"}},
				Agent:      &mdbv1.X509Subject{Organizations: []string{"same"}},
			},
			expOK: false,
		},
		{
			name: "internal and agent auth x509 with the same O but different OU is allowed",
			auth: auth(true, true),
			subs: &mdbv1.CertSubjects{
				Membership: &mdbv1.X509Subject{Organizations: []string{"same"}, OrganizationalUnits: []string{"ou-a"}},
				Agent:      &mdbv1.X509Subject{Organizations: []string{"same"}, OrganizationalUnits: []string{"ou-b"}},
			},
			expOK: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
				m.Spec.Security.Authentication = tc.auth
				m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true, Subjects: tc.subs}
			})
			assert.Equal(t, tc.expOK, verifyAgentSubjectDistinct(owner).IsOK())
		})
	}
}

func TestComponentCertificates(t *testing.T) {
	ctx := context.Background()
	// No member Certificate is seeded, so readCoveredMembersCount returns 0; the SAN
	// count comes straight from MembersToCover/Replicas. The never-shrink hold behaviour is
	// covered by TestResolveMembersToCover.
	fakeClient, _ := mock.NewDefaultFakeClient()
	baseOpts := Options{
		ResourceName:   resName,
		CertSecretName: fmt.Sprintf("%s-cert", resName),
		Namespace:      resNamespace,
		ServiceName:    fmt.Sprintf("%s-svc", resName),
		Replicas:       1,
		ClusterDomain:  "cluster.local",
	}

	t.Run("keyfile internal auth: only the member (serverAuth) cert, SAN-only subject", func(t *testing.T) {
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true}
		})
		got := componentCertificates(ctx, fakeClient, owner, baseOpts)
		require.Len(t, got, 1)
		assert.Equal(t, baseOpts.CertSecretName, got[0].certName)
		assert.Equal(t, memberServerCertUsages, got[0].spec.Usages)
		assert.Nil(t, got[0].spec.Subject, "member cert is SAN-only when internal auth is not x509")
	})

	t.Run("x509 internal auth: member + clusterfile, both carry the same membership subject", func(t *testing.T) {
		opts := baseOpts
		opts.InternalClusterSecretName = fmt.Sprintf("%s-clusterfile", resName)
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true}
			m.Spec.Security.Authentication = &mdbv1.Authentication{InternalCluster: util.X509}
		})
		got := componentCertificates(ctx, fakeClient, owner, opts)
		require.Len(t, got, 2)

		// member cert: serverAuth, membership subject
		assert.Equal(t, baseOpts.CertSecretName, got[0].certName)
		assert.Equal(t, memberServerCertUsages, got[0].spec.Usages)
		require.NotNil(t, got[0].spec.Subject)
		assert.Equal(t, []string{fmt.Sprintf("%s-server", resName)}, got[0].spec.Subject.Organizations)

		// clusterfile cert: clientAuth, identical membership subject
		assert.Equal(t, opts.InternalClusterSecretName, got[1].certName)
		assert.Equal(t, clientAuthCertUsages, got[1].spec.Usages)
		assert.Equal(t, got[0].spec.Subject, got[1].spec.Subject, "member and clusterfile must share the membership subject")

		// The clusterfile carries no SANs (mongod matches it by subject, not SAN), so it must
		// carry a common name instead - cert-manager rejects a Certificate with neither.
		assert.Empty(t, got[1].spec.DNSNames, "clusterfile cert must not have per-member SANs")
		assert.NotEmpty(t, got[1].spec.CommonName, "clusterfile cert needs a common name when it has no SANs")
	})

	t.Run("member cert SANs cover MembersToCover and the count is recorded in the annotation", func(t *testing.T) {
		opts := baseOpts
		opts.Replicas = 3
		opts.MembersToCover = 5
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true}
		})
		got := componentCertificates(ctx, fakeClient, owner, opts)
		require.Len(t, got, 1)
		sans := strings.Join(got[0].spec.DNSNames, " ")
		for i := range 5 {
			assert.Contains(t, sans, fmt.Sprintf("%s-%d.", resName, i), "member %d SAN must be present", i)
		}
		// The count is remembered in the annotation so a later reconcile can hold the SANs steady.
		assert.Equal(t, "5", got[0].annotations[coveredMembersAnnotation])
	})
}

func TestResolveMembersToCover(t *testing.T) {
	tests := []struct {
		name           string
		opts           Options
		coveredMembers int
		expected       int
	}{
		{
			name:     "steady state uses MembersToCover",
			opts:     Options{Replicas: 3, MembersToCover: 3},
			expected: 3,
		},
		{
			name:     "scale-up jumps to the target immediately",
			opts:     Options{Replicas: 4, MembersToCover: 5},
			expected: 5,
		},
		{
			name:           "scale-down holds the covered count, it does not shrink",
			opts:           Options{Replicas: 4, MembersToCover: 4},
			coveredMembers: 5,
			expected:       5,
		},
		{
			name:           "settled below a past peak still holds it (never shrink)",
			opts:           Options{Replicas: 3, MembersToCover: 3},
			coveredMembers: 5,
			expected:       5,
		},
		{
			name:           "scale-up beyond the covered count grows to the target",
			opts:           Options{Replicas: 6, MembersToCover: 6},
			coveredMembers: 5,
			expected:       6,
		},
		{
			name:     "no MembersToCover falls back to Replicas",
			opts:     Options{Replicas: 3},
			expected: 3,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, resolveMembersToCover(tc.opts, tc.coveredMembers))
		})
	}
}

// TestAgentCertificate covers that the agent cert is issued only under x509 agent auth, with
// clientAuth usages and the agent subject.
func TestAgentCertificate(t *testing.T) {
	t.Run("agent auth x509: issued with clientAuth and the agent subject", func(t *testing.T) {
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true}
			m.Spec.Security.Authentication = &mdbv1.Authentication{Enabled: true, Modes: []mdbv1.AuthMode{mdbv1.AuthMode(util.X509)}}
		})
		cert, ok := agentCertificate(owner)
		require.True(t, ok)
		assert.Equal(t, fmt.Sprintf("%s-agent-certs", resName), cert.certName)
		assert.Equal(t, clientAuthCertUsages, cert.spec.Usages)
		require.NotNil(t, cert.spec.Subject)
		assert.Equal(t, []string{fmt.Sprintf("%s-agent", resName)}, cert.spec.Subject.Organizations)
	})

	t.Run("agent auth not x509: no agent cert", func(t *testing.T) {
		owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
			m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{Enabled: true}
		})
		_, ok := agentCertificate(owner)
		assert.False(t, ok)
	})
}

// TestResolveUserCA covers how the operator finds the CA that verifies a category's certs when
// that category is signed by a user issuer.
func TestResolveUserCA(t *testing.T) {
	ctx := context.Background()
	issuedName := fmt.Sprintf("%s-cert", resName)

	newSecret := func(name string, data map[string][]byte) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resNamespace}, Data: data}
	}
	newConfigMap := func(name string, data map[string]string) *corev1.ConfigMap {
		return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: resNamespace}, Data: data}
	}

	tests := []struct {
		name             string
		objects          []client.Object
		userConfiguredCA *mdbv1.CARef
		expCA            string
		expOK            bool
		expMessage       string
	}{
		{
			name:    "configured issuer emits its ca.crt so it is used directly",
			objects: []client.Object{newSecret(issuedName, map[string][]byte{corev1.ServiceAccountRootCAKey: []byte("issuer-ca")})},
			expCA:   "issuer-ca",
			expOK:   true,
		},
		{
			name:       "issued certificate secret does not exist yet so we wait",
			objects:    nil,
			expOK:      false,
			expMessage: "Waiting for cert-manager to write the certificate secret",
		},
		{
			name: "no ca.crt key in certificate secret, so we read the CA from the ConfigMap the user configured",
			objects: []client.Object{
				newSecret(issuedName, map[string][]byte{corev1.TLSCertKey: []byte("leaf")}),
				newConfigMap("user-ca", map[string]string{"ca.crt": "configmap-ca"}),
			},
			userConfiguredCA: &mdbv1.CARef{Name: "user-ca", Kind: "ConfigMap", Key: "ca.crt"},
			expCA:            "configmap-ca",
			expOK:            true,
		},
		{
			name: "no ca.crt key in certificate secret, so we read the CA from the Secret the user configured",
			objects: []client.Object{
				newSecret(issuedName, map[string][]byte{}),
				newSecret("user-ca", map[string][]byte{"ca.crt": []byte("secret-ca")}),
			},
			userConfiguredCA: &mdbv1.CARef{Name: "user-ca", Kind: "Secret", Key: "ca.crt"},
			expCA:            "secret-ca",
			expOK:            true,
		},
		{
			name:       "no ca.crt in certificate secret and the user gave no CA using CR so we fail",
			objects:    []client.Object{newSecret(issuedName, map[string][]byte{})},
			expOK:      false,
			expMessage: "issuer did not populate ca.crt in the certificate secret and no CA was provided via security.managedCertificate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owner := newMongoDB(resName, func(m *mdbv1.MongoDB) {
				m.Spec.Security.ManagedCertificate = &mdbv1.ManagedCertificate{
					Enabled: true,
					Server:  &mdbv1.CertificateConfig{CA: tc.userConfiguredCA},
				}
			})
			fakeClient, _ := mock.NewDefaultFakeClient(tc.objects...)

			ca, status := resolveUserCA(ctx, fakeClient, owner, categoryServer, issuedName)

			assert.Equal(t, tc.expOK, status.IsOK())
			if tc.expOK {
				assert.Equal(t, tc.expCA, ca)
			} else {
				assert.Empty(t, ca)
				assert.Contains(t, status.Message(), tc.expMessage)
			}
		})
	}
}
