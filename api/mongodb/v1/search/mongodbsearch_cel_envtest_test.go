package search_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	searchv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/search"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/user"
	"github.com/mongodb/mongodb-kubernetes/test/envtest/env"
)

// TestMain boots one envtest control plane shared by all tests in this package
// (see test/envtest/env). Future envtest-based tests in this package should use
// env.Shared(t) instead of starting their own environment.
func TestMain(m *testing.M) {
	os.Exit(env.RunShared(m, env.WithCRDs("mongodb.com_mongodbsearch.yaml")))
}

// TestMongoDBSearchCELValidation proves that the CEL validation rules defined on
// the MongoDBSearch CRD are enforced by a real Kubernetes API server (booted
// locally via envtest). It covers both create-time rules and the oldSelf-based
// transition rule, neither of which can be exercised by plain Go unit tests.
func TestMongoDBSearchCELValidation(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newSearch := func(clusters ...searchv1.ClusterSpec) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-", Namespace: "default"},
			Spec:       searchv1.MongoDBSearchSpec{Clusters: clusters},
		}
	}

	tests := []struct {
		name string
		// create is the spec.clusters value submitted on create.
		create []searchv1.ClusterSpec
		// update, when non-nil, replaces spec.clusters in an update after a
		// successful create; the expectation then applies to that update
		// (for oldSelf-based transition rules).
		update []searchv1.ClusterSpec
		// errorContains is the expected CEL validation message;
		// empty means the operation must succeed.
		errorContains string
	}{
		{
			name:   "valid minimal spec is accepted",
			create: []searchv1.ClusterSpec{{}},
		},
		{
			name: "loadBalancer must set exactly one of managed or unmanaged",
			create: []searchv1.ClusterSpec{{
				LoadBalancer: &searchv1.LoadBalancerConfig{
					Managed:   &searchv1.ManagedLBConfig{},
					Unmanaged: &searchv1.UnmanagedLBConfig{},
				},
			}},
			errorContains: "exactly one of managed or unmanaged must be set",
		},
		{
			name: "cluster names are required when more than one cluster is specified",
			create: []searchv1.ClusterSpec{
				{Index: new(int32(0))},
				{Index: new(int32(1))},
			},
			errorContains: "clusters[].name must be set and unique when more than one cluster is specified",
		},
		{
			name: "cluster index must be unique",
			create: []searchv1.ClusterSpec{
				{Name: "cluster-a", Index: new(int32(0))},
				{Name: "cluster-b", Index: new(int32(0))},
			},
			errorContains: "clusters[].index must be unique when set",
		},
		{
			name:          "cluster name is immutable for an existing index",
			create:        []searchv1.ClusterSpec{{Name: "cluster-a", Index: new(int32(0))}},
			update:        []searchv1.ClusterSpec{{Name: "cluster-b", Index: new(int32(0))}},
			errorContains: "clusters[].name is immutable for an existing cluster index",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			search := newSearch(tc.create...)
			err := k8sClient.Create(ctx, search)

			if tc.update != nil {
				require.NoError(t, err)
				search.Spec.Clusters = tc.update
				err = k8sClient.Update(ctx, search)
			}

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}

// TestMongoDBSearchCELValidationSourceOIDC proves the spec.source.oidc CEL rules on a
// real Kubernetes API server (envtest), like TestMongoDBSearchCELValidation.
func TestMongoDBSearchCELValidationSourceOIDC(t *testing.T) {
	ctx := context.Background()
	k8sClient := env.Shared(t).Client

	newOIDCSearch := func(source *searchv1.MongoDBSource) *searchv1.MongoDBSearch {
		return &searchv1.MongoDBSearch{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-cel-oidc-", Namespace: "default"},
			Spec:       searchv1.MongoDBSearchSpec{Clusters: []searchv1.ClusterSpec{{}}, Source: source},
		}
	}

	validOIDC := func() *searchv1.OIDCAuth {
		return &searchv1.OIDCAuth{
			TokenEndpoint:     "https://idp.example.com/realms/search/protocol/openid-connect/token",
			ClientID:          "search-sync-source",
			ClientSecretRef:   userv1.SecretKeyRef{Name: "my-oidc-client-secret", Key: "clientSecret"},
			IdPCAConfigMapRef: corev1.LocalObjectReference{Name: "my-idp-ca"},
		}
	}

	// One row per rule the CRD CEL markers enforce, with mutate fields instead of per-case
	// closures. TestValidateOIDCAuthConfig pins the Go backstop branches for the same rules.
	tests := []struct {
		name          string
		mutateOIDC    func(*searchv1.OIDCAuth)
		mutateSource  func(*searchv1.MongoDBSource)
		errorContains string
	}{
		{name: "valid spec.source.oidc is accepted"},
		{
			name:          "tokenEndpoint with a non-https scheme is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "http://idp.example.com/token" },
			errorContains: "tokenEndpoint must be an absolute HTTPS URI with a host and no userinfo or fragment",
		},
		{
			name:          "tokenEndpoint with an empty host is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "https:///token" },
			errorContains: "tokenEndpoint must be an absolute HTTPS URI with a host and no userinfo or fragment",
		},
		{
			name:          "tokenEndpoint with a port-only authority is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "https://:443/token" },
			errorContains: "tokenEndpoint must be an absolute HTTPS URI with a host and no userinfo or fragment",
		},
		{
			name:          "tokenEndpoint with a non-numeric port is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "https://idp.example.com:port/token" },
			errorContains: "tokenEndpoint must be an absolute HTTPS URI with a host and no userinfo or fragment",
		},
		{
			name:       "tokenEndpoint with @ in the path is accepted",
			mutateOIDC: func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "https://idp.example.com/oauth2/v2/token@alias" },
		},
		{
			name:       "tokenEndpoint with a port and query is accepted",
			mutateOIDC: func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "https://idp.example.com:8443/oauth2/token?x=1" },
		},
		{
			// The scheme is matched case-insensitively, matching url.Parse on the Go side.
			name:       "tokenEndpoint with an uppercase https scheme is accepted",
			mutateOIDC: func(o *searchv1.OIDCAuth) { o.TokenEndpoint = "HTTPS://idp.example.com/token" },
		},
		{
			name: "oidc and x509 are mutually exclusive",
			mutateSource: func(src *searchv1.MongoDBSource) {
				src.X509 = &searchv1.X509Auth{ClientCertificateSecret: corev1.LocalObjectReference{Name: "my-cert"}}
			},
			errorContains: "spec.source.oidc is mutually exclusive with spec.source.x509, spec.source.passwordSecretRef, and spec.source.username",
		},
		{
			name: "oidc and passwordSecretRef are mutually exclusive",
			mutateSource: func(src *searchv1.MongoDBSource) {
				src.PasswordSecretRef = &userv1.SecretKeyRef{Name: "my-password"}
			},
			errorContains: "spec.source.oidc is mutually exclusive with spec.source.x509, spec.source.passwordSecretRef, and spec.source.username",
		},
		{
			name: "oidc and username are mutually exclusive",
			mutateSource: func(src *searchv1.MongoDBSource) {
				src.Username = new("some-user")
			},
			errorContains: "spec.source.oidc is mutually exclusive with spec.source.x509, spec.source.passwordSecretRef, and spec.source.username",
		},
		{
			name:          "clientSecretSecretRef without key is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.ClientSecretRef = userv1.SecretKeyRef{Name: "my-oidc-client-secret"} },
			errorContains: "clientSecretSecretRef.name, clientSecretSecretRef.key, and idpCAConfigMapRef.name must not be empty",
		},
		{
			name:          "idpCAConfigMapRef with an empty name is rejected",
			mutateOIDC:    func(o *searchv1.OIDCAuth) { o.IdPCAConfigMapRef = corev1.LocalObjectReference{} },
			errorContains: "clientSecretSecretRef.name, clientSecretSecretRef.key, and idpCAConfigMapRef.name must not be empty",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oidc := validOIDC()
			if tc.mutateOIDC != nil {
				tc.mutateOIDC(oidc)
			}
			source := &searchv1.MongoDBSource{OIDC: oidc}
			if tc.mutateSource != nil {
				tc.mutateSource(source)
			}

			search := newOIDCSearch(source)
			err := k8sClient.Create(ctx, search)

			if tc.errorContains == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.True(t, apierrors.IsInvalid(err), "expected an Invalid error, got: %v", err)
			assert.Contains(t, err.Error(), tc.errorContains)
		})
	}
}
