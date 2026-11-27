package certs

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	cmmeta "github.com/cert-manager/cert-manager/pkg/apis/meta/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSetConditionIssuing(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	earlier := metav1.NewTime(now.Add(-time.Hour))

	t.Run("replaces an existing Issuing=False condition in place", func(t *testing.T) {
		ready := certmanagerv1.CertificateCondition{
			Type:               certmanagerv1.CertificateConditionReady,
			Status:             cmmeta.ConditionTrue,
			Reason:             "Ready",
			LastTransitionTime: &earlier,
			ObservedGeneration: 2,
		}
		cert := &certmanagerv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{Name: "leaf", Generation: 3},
			Status: certmanagerv1.CertificateStatus{Conditions: []certmanagerv1.CertificateCondition{
				ready,
				{
					Type:               certmanagerv1.CertificateConditionIssuing,
					Status:             cmmeta.ConditionFalse,
					Reason:             "Done",
					Message:            "previous issuance finished",
					LastTransitionTime: &earlier,
					ObservedGeneration: 2,
				},
			}},
		}

		setConditionIssuing(cert, now, reasonManagedCARotated, "rotated")

		require.Len(t, cert.Status.Conditions, 2, "the Issuing condition must be replaced, not duplicated")
		assert.Equal(t, ready, cert.Status.Conditions[0], "the Ready condition must be kept unchanged")

		issuing := cert.Status.Conditions[1]
		assert.Equal(t, certmanagerv1.CertificateConditionIssuing, issuing.Type)
		assert.Equal(t, cmmeta.ConditionTrue, issuing.Status)
		assert.Equal(t, reasonManagedCARotated, issuing.Reason)
		assert.Equal(t, "rotated", issuing.Message)
		require.NotNil(t, issuing.LastTransitionTime)
		assert.True(t, issuing.LastTransitionTime.Equal(&now))
		assert.Equal(t, int64(3), issuing.ObservedGeneration)
		assert.True(t, isConditionIssuing(cert))
	})

	t.Run("appends the Issuing condition when there is none", func(t *testing.T) {
		cert := &certmanagerv1.Certificate{
			ObjectMeta: metav1.ObjectMeta{Name: "leaf", Generation: 1},
			Status: certmanagerv1.CertificateStatus{Conditions: []certmanagerv1.CertificateCondition{
				{Type: certmanagerv1.CertificateConditionReady, Status: cmmeta.ConditionTrue},
			}},
		}

		setConditionIssuing(cert, now, reasonManagedCARotated, "rotated")

		require.Len(t, cert.Status.Conditions, 2)
		assert.Equal(t, certmanagerv1.CertificateConditionReady, cert.Status.Conditions[0].Type)
		assert.Equal(t, certmanagerv1.CertificateConditionIssuing, cert.Status.Conditions[1].Type)
		assert.Equal(t, cmmeta.ConditionTrue, cert.Status.Conditions[1].Status)
		assert.Equal(t, int64(1), cert.Status.Conditions[1].ObservedGeneration)
	})
}

func TestIsConditionIssuing(t *testing.T) {
	for _, tc := range []struct {
		name       string
		conditions []certmanagerv1.CertificateCondition
		expected   bool
	}{
		{name: "no conditions", expected: false},
		{
			name:       "only Ready",
			conditions: []certmanagerv1.CertificateCondition{{Type: certmanagerv1.CertificateConditionReady, Status: cmmeta.ConditionTrue}},
			expected:   false,
		},
		{
			name: "Issuing=True",
			conditions: []certmanagerv1.CertificateCondition{
				{Type: certmanagerv1.CertificateConditionReady, Status: cmmeta.ConditionTrue},
				{Type: certmanagerv1.CertificateConditionIssuing, Status: cmmeta.ConditionTrue},
			},
			expected: true,
		},
		{
			name:       "Issuing=False",
			conditions: []certmanagerv1.CertificateCondition{{Type: certmanagerv1.CertificateConditionIssuing, Status: cmmeta.ConditionFalse}},
			expected:   false,
		},
		{
			name:       "Issuing=Unknown",
			conditions: []certmanagerv1.CertificateCondition{{Type: certmanagerv1.CertificateConditionIssuing, Status: cmmeta.ConditionUnknown}},
			expected:   false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cert := &certmanagerv1.Certificate{Status: certmanagerv1.CertificateStatus{Conditions: tc.conditions}}
			assert.Equal(t, tc.expected, isConditionIssuing(cert))
		})
	}
}
