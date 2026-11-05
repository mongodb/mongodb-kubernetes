package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	testNamespace = "test-namespace"
	testUUID      = "123e4567-e89b-12d3-a456-426614174000"
)

type failingFakeClient struct {
	kubeclient.Client
}

func (f *failingFakeClient) Get(ctx context.Context, key kubeclient.ObjectKey, obj kubeclient.Object, opts ...kubeclient.GetOption) error {
	return fmt.Errorf("simulated API failure")
}

type failingUpdateFakeClient struct {
	kubeclient.Client
}

func (f *failingUpdateFakeClient) Update(ctx context.Context, obj kubeclient.Object, opts ...kubeclient.UpdateOption) error {
	return fmt.Errorf("simulated update failure")
}

type failingCreateFakeClient struct {
	kubeclient.Client
}

func (f *failingCreateFakeClient) Create(ctx context.Context, obj kubeclient.Object, opts ...kubeclient.CreateOption) error {
	return fmt.Errorf("simulated create failure")
}

func TestGetOrGenerateOperatorUUID(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	ctx := context.Background()

	t.Run("should return existing UUID from ConfigMap", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      OperatorConfigMapTelemetryConfigMapName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				UUIDKey: testUUID,
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()
		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.Equal(t, testUUID, result, "Expected to return the existing UUID")
	})

	t.Run("should create ConfigMap if not present", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.NotEmpty(t, result, "Expected a new UUID to be generated")

		createdCM := &corev1.ConfigMap{}
		err := client.Get(ctx, kubeclient.ObjectKey{Namespace: testNamespace, Name: OperatorConfigMapTelemetryConfigMapName}, createdCM)
		assert.NoError(t, err, "Expected the ConfigMap to be created")
		assert.Equal(t, result, createdCM.Data[UUIDKey], "Expected the new UUID to be stored in the ConfigMap")
	})

	t.Run("should update ConfigMap if required fields are missing", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      OperatorConfigMapTelemetryConfigMapName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				UUIDKey: testUUID,
			},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()
		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.Equal(t, testUUID, result, "Expected to return the existing UUID")

		// Verify ConfigMap has been updated with missing fields
		updatedCM := &corev1.ConfigMap{}
		err := client.Get(ctx, kubeclient.ObjectKey{Namespace: testNamespace, Name: OperatorConfigMapTelemetryConfigMapName}, updatedCM)
		assert.NoError(t, err, "Expected the ConfigMap to be updated")
		assert.Contains(t, updatedCM.Data, Operators.GetTimeStampKey(), "Expected missing keys to be added")
		assert.Contains(t, updatedCM.Data, Deployments.GetTimeStampKey(), "Expected missing keys to be added")
		assert.Contains(t, updatedCM.Data, Clusters.GetTimeStampKey(), "Expected missing keys to be added")
	})

	t.Run("should generate and update UUID if missing", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      OperatorConfigMapTelemetryConfigMapName,
				Namespace: testNamespace,
			},
			Data: map[string]string{}, // No UUID key present
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()
		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.NotEmpty(t, result, "Expected a new UUID to be generated")

		// Verify UUID is now stored in the ConfigMap
		updatedCM := &corev1.ConfigMap{}
		err := client.Get(ctx, kubeclient.ObjectKey{Namespace: testNamespace, Name: OperatorConfigMapTelemetryConfigMapName}, updatedCM)
		assert.NoError(t, err, "Expected the ConfigMap to be updated")
		assert.Equal(t, result, updatedCM.Data[UUIDKey], "Expected the new UUID to be stored in the ConfigMap")
	})

	t.Run("should return unknown if ConfigMap retrieval fails", func(t *testing.T) {
		client := &failingFakeClient{} // Simulate API failure

		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.Equal(t, unknown, result, "Expected 'unknown' when ConfigMap retrieval fails")
	})

	t.Run("should return unknown if updating ConfigMap with missing keys fails", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      OperatorConfigMapTelemetryConfigMapName,
				Namespace: testNamespace,
			},
			Data: map[string]string{
				UUIDKey: testUUID,
			},
		}

		client := &failingUpdateFakeClient{fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()} // Simulate update failure

		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.Equal(t, unknown, result, "Expected 'unknown' when ConfigMap update fails")
	})

	t.Run("should return unknown if creating a new ConfigMap fails", func(t *testing.T) {
		client := &failingCreateFakeClient{fake.NewClientBuilder().WithScheme(scheme).Build()} // Simulate create failure

		result := getOrGenerateOperatorUUID(ctx, client, testNamespace)

		assert.Equal(t, unknown, result, "Expected 'unknown' when ConfigMap creation fails")
	})
}

func TestUpdateTelemetryConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	ctx := context.Background()
	eventType := Deployments
	testEvents := []Event{{
		Timestamp:  time.Now(),
		Source:     Deployments,
		Properties: map[string]any{"test": "b"},
	}}

	t.Run("should update existing ConfigMap with timestamp", func(t *testing.T) {
		existingCM := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      OperatorConfigMapTelemetryConfigMapName,
				Namespace: testNamespace,
			},
			Data: map[string]string{},
		}

		client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existingCM).Build()

		err := updateTelemetryConfigMapTimeStamp(ctx, client, testNamespace, OperatorConfigMapTelemetryConfigMapName, eventType)
		assert.NoError(t, err, "Expected update to succeed")
		err = updateTelemetryConfigMapPayload(ctx, client, testEvents, testNamespace, OperatorConfigMapTelemetryConfigMapName, eventType)
		assert.NoError(t, err, "Expected update to succeed")

		updatedCM := &corev1.ConfigMap{}
		err = client.Get(ctx, kubeclient.ObjectKey{Name: OperatorConfigMapTelemetryConfigMapName, Namespace: testNamespace}, updatedCM)
		assert.NoError(t, err, "Expected to retrieve updated ConfigMap")

		timestamp, err := strconv.ParseInt(updatedCM.Data[Deployments.GetTimeStampKey()], 10, 64)
		assert.NoError(t, err, "Expected timestamp to be a valid integer")
		assert.Greater(t, timestamp, int64(0), "Expected a non-zero timestamp")

		var storedEvents []Event
		err = json.Unmarshal([]byte(updatedCM.Data[Deployments.GetPayloadKey()]), &storedEvents)
		assert.NoError(t, err, "Expected payload to be valid JSON")
		assert.Equal(t, testEvents[0].Properties, storedEvents[0].Properties, "Expected stored events to match")
		assert.Equal(t, testEvents[0].Source, storedEvents[0].Source, "Expected stored events to match")
	})

	t.Run("should fail if ConfigMap does not exist", func(t *testing.T) {
		client := fake.NewClientBuilder().WithScheme(scheme).Build()

		err := updateTelemetryConfigMapTimeStamp(ctx, client, testNamespace, OperatorConfigMapTelemetryConfigMapName, eventType)
		assert.Error(t, err, "Expected error when ConfigMap does not exist")
		assert.Contains(t, err.Error(), "failed to get ConfigMap", "Expected get ConfigMap error message")
	})
}

func TestIsTimestampOlderThanConfiguredFrequency(t *testing.T) {
	ctx := context.Background()

	namespace := "test-namespace"
	configMapName := "test-configmap"

	et := Deployments

	tests := []struct {
		name             string
		frequencySetting string
		configMapData    map[string]string
		shouldCollect    bool
		expectedErr      bool
		description      string
	}{
		{
			name: "Default one-week check - outdated",
			configMapData: map[string]string{
				et.GetTimeStampKey(): strconv.FormatInt(time.Now().Add(-8*24*time.Hour).Unix(), 10), // 8 days ago
			},
			shouldCollect: true,
			expectedErr:   false,
			description:   "Timestamp is older than one week",
		},
		{
			name: "Default one-week check - recent",
			configMapData: map[string]string{
				et.GetTimeStampKey(): strconv.FormatInt(time.Now().Add(-6*24*time.Hour).Unix(), 10), // 6 days ago
			},
			shouldCollect: false,
			expectedErr:   false,
			description:   "Timestamp is within one week",
		},
		{
			name:             "Custom duration from env - below minimum, will default to 1h",
			frequencySetting: "5m",
			configMapData: map[string]string{
				et.GetTimeStampKey(): strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10),
			},
			shouldCollect: false,
			expectedErr:   false,
			description:   "Timestamp is older than configured 5-minute threshold",
		},
		{
			name:             "Custom duration from env - recent",
			frequencySetting: "30m",
			configMapData: map[string]string{
				et.GetTimeStampKey(): strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10),
			},
			shouldCollect: false,
			expectedErr:   false,
			description:   "Timestamp is within configured 30-minute threshold",
		},
		{
			name:             "Invalid duration format",
			frequencySetting: "invalid",
			configMapData: map[string]string{
				et.GetTimeStampKey(): strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10),
			},
			shouldCollect: false,
			expectedErr:   false,
			description:   "Should default to 168h",
		},
		{
			name: "Missing timestamp key",
			configMapData: map[string]string{
				"someOtherKey": "1650000000",
			},
			shouldCollect: false,
			expectedErr:   true,
			description:   "Should return error due to missing timestamp key",
		},
		{
			name: "Initial timestamp value",
			configMapData: map[string]string{
				et.GetTimeStampKey(): TimestampInitialValue,
			},
			shouldCollect: true,
			expectedErr:   false,
			description:   "Should return true for initial timestamp",
		},
		{
			name: "Invalid timestamp format",
			configMapData: map[string]string{
				et.GetTimeStampKey(): "invalid_timestamp",
			},
			shouldCollect: false,
			expectedErr:   true,
			description:   "Should return error due to invalid timestamp format",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv(SendFrequency, test.frequencySetting)

			fakeClient := fake.NewClientBuilder().
				WithObjects(&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      configMapName,
						Namespace: namespace,
					},
					Data: test.configMapData,
				}).
				Build()

			result, err := isTimestampOlderThanConfiguredFrequency(ctx, fakeClient, namespace, configMapName, et)

			assert.Equal(t, test.shouldCollect, result, test.description)

			if test.expectedErr {
				assert.Error(t, err, test.description)
			} else {
				assert.NoError(t, err, test.description)
			}
		})
	}
}
