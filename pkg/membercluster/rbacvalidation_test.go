package membercluster

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

const (
	testSAName      = "mck-member-cluster-a-sa"
	testSANamespace = "mongodb"
	testExpected    = "1.6.0"
)

func memberServiceAccount(annotations map[string]string) *corev1.ServiceAccount {
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{Name: testSAName, Namespace: testSANamespace, Annotations: annotations},
	}
}

func staticValidator(c client.Client) rbacValidator {
	return rbacValidator{newClient: func(*restclient.Config) (client.Client, error) { return c, nil }}
}

// getErrorInterceptors makes every GET on a fake client fail with err.
func getErrorInterceptors(err error) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
			return err
		},
	}
}

func TestProbeClassification(t *testing.T) {
	tests := []struct {
		name           string
		serviceAccount *corev1.ServiceAccount
		interceptors   interceptor.Funcs
		expected       string
		wantStatus     metav1.ConditionStatus
		wantReason     string
		wantMessage    string
	}{
		{
			name:           "matching annotation",
			serviceAccount: memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: testExpected}),
			expected:       testExpected,
			wantStatus:     metav1.ConditionTrue,
			wantReason:     reasonValid,
			wantMessage:    `RBAC version "1.6.0" matches the operator's expected version.`,
		},
		{
			name:           "mismatching annotation",
			serviceAccount: memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: "1.5.0"}),
			expected:       testExpected,
			wantStatus:     metav1.ConditionFalse,
			wantReason:     reasonVersionMismatch,
			wantMessage:    `RBAC version "1.5.0" on the member cluster does not match the operator's expected version "1.6.0". Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.`,
		},
		{
			name:           "empty annotation value",
			serviceAccount: memberServiceAccount(map[string]string{util.MemberClusterRBACVersionAnnotation: ""}),
			expected:       testExpected,
			wantStatus:     metav1.ConditionFalse,
			wantReason:     reasonVersionMismatch,
		},
		{
			name:           "annotation missing",
			serviceAccount: memberServiceAccount(nil),
			expected:       testExpected,
			wantStatus:     metav1.ConditionFalse,
			wantReason:     reasonRBACVersionMissing,
		},
		{
			name:        "serviceaccount absent",
			expected:    testExpected,
			wantStatus:  metav1.ConditionFalse,
			wantReason:  reasonMemberServiceAccountNotFound,
			wantMessage: `ServiceAccount "mck-member-cluster-a-sa" not found in namespace "mongodb" of the member cluster. Regenerate and reapply member-cluster RBAC with 'kubectl mongodb multicluster generate-member-resources'.`,
		},
		{
			name:         "forbidden",
			interceptors: getErrorInterceptors(apierrors.NewForbidden(schema.GroupResource{Resource: "serviceaccounts"}, testSAName, errors.New("denied"))),
			expected:     testExpected,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   reasonProbeForbidden,
		},
		{
			name:         "unauthorized",
			interceptors: getErrorInterceptors(apierrors.NewUnauthorized("token revoked")),
			expected:     testExpected,
			wantStatus:   metav1.ConditionFalse,
			wantReason:   reasonCredentialInvalid,
		},
		{
			name:         "transient error",
			interceptors: getErrorInterceptors(errors.New("connection refused")),
			expected:     testExpected,
			wantStatus:   metav1.ConditionUnknown,
			wantReason:   reasonProbeFailed,
			wantMessage:  `Failed to read ServiceAccount "mck-member-cluster-a-sa" in namespace "mongodb" from the member cluster: connection refused`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(testScheme()).WithInterceptorFuncs(tt.interceptors)
			if tt.serviceAccount != nil {
				builder = builder.WithObjects(tt.serviceAccount)
			}
			v := staticValidator(builder.Build())

			outcome := v.Probe(t.Context(), &restclient.Config{}, testSAName, testSANamespace, tt.expected)

			assert.Equal(t, tt.wantStatus, outcome.status)
			assert.Equal(t, tt.wantReason, outcome.reason)
			if tt.wantMessage != "" {
				assert.Equal(t, tt.wantMessage, outcome.message)
			}
		})
	}
}

func TestProbeClientBuildFailureIsTransient(t *testing.T) {
	v := rbacValidator{newClient: func(*restclient.Config) (client.Client, error) {
		return nil, errors.New("invalid config")
	}}

	outcome := v.Probe(t.Context(), &restclient.Config{}, testSAName, testSANamespace, testExpected)
	assert.Equal(t, metav1.ConditionUnknown, outcome.status)
	assert.Equal(t, reasonProbeFailed, outcome.reason)
	assert.Contains(t, outcome.message, "invalid config")
}

// The real client builder must produce a working direct client for a rest.Config.
func TestNewRBACValidatorBuildsClient(t *testing.T) {
	c, err := newRBACValidator().newClient(&restclient.Config{Host: "https://member.example.com:6443"})
	require.NoError(t, err)
	require.NotNil(t, c)
}
