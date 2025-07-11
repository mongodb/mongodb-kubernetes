package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"k8s.io/apimachinery/pkg/types"

	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	v2 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/util/envvar"
)

const (
	TimestampKey          = "lastSendTimestamp"
	TimestampInitialValue = "initialValue"
	LastSendPayloadKey    = "lastSendPayload"
	UUIDKey               = "operatorUUID"
)

func getOrGenerateOperatorUUID(ctx context.Context, k8sClient kubeclient.Client, namespace string) string {
	configMap := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, kubeclient.ObjectKey{Namespace: namespace, Name: OperatorConfigMapTelemetryConfigMapName}, configMap)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			// ConfigMap does not exist -> Create a new one
			Logger.Debugf("ConfigMap %s not found. Creating a new one.", OperatorConfigMapTelemetryConfigMapName)
			return createNewConfigMap(ctx, k8sClient, namespace)
		}

		// Any other error (e.g., network issue, permissions issue)
		Logger.Errorf("Failed to get ConfigMap %s: %v", OperatorConfigMapTelemetryConfigMapName, err)
		return unknown
	}

	// If ConfigMap exists, check for UUID and required keys
	if existingUUID, exists := configMap.Data[UUIDKey]; exists {
		// Ensure all event timestamps are present
		if addMissingKeys(configMap) {
			if updateErr := k8sClient.Update(ctx, configMap); updateErr != nil {
				Logger.Debugf("Failed to update ConfigMap %s with missing keys", OperatorConfigMapTelemetryConfigMapName)
				return unknown
			}
		}
		return existingUUID
	}

	// UUID is missing; generate and update
	Logger.Debugf("ConfigMap %s exists but lacks a UUID, generating one", OperatorConfigMapTelemetryConfigMapName)
	return updateConfigMapWithNewUUID(ctx, k8sClient, namespace)
}

// Adds missing timestamp keys to the ConfigMap. Returns true if updates were made.
func addMissingKeys(configMap *corev1.ConfigMap) bool {
	updated := false
	for _, et := range AllEventTypes {
		key := et.GetTimeStampKey()
		if _, exists := configMap.Data[key]; !exists {
			configMap.Data[key] = TimestampKey
			updated = true
		}
	}
	return updated
}

// Updates an existing ConfigMap with a new UUID
func updateConfigMapWithNewUUID(ctx context.Context, k8sClient kubeclient.Client, namespace string) string {
	newUUID, newConfigMap := createInitialConfigmap(namespace)
	if err := k8sClient.Update(ctx, newConfigMap); err != nil {
		Logger.Debugf("Failed to update ConfigMap %s with new UUID", OperatorConfigMapTelemetryConfigMapName)
		return unknown
	}
	return newUUID
}

// Creates a new ConfigMap with a generated UUID
func createNewConfigMap(ctx context.Context, k8sClient kubeclient.Client, namespace string) string {
	ctx, span := TRACER.Start(ctx, "createNewConfigMap")
	span.SetAttributes(
		attribute.String("mck.resource.type", "telemetry-collection"),
		attribute.String("mck.k8s.namespace", namespace),
	)
	defer span.End()

	newUUID, newConfigMap := createInitialConfigmap(namespace)
	if err := k8sClient.Create(ctx, newConfigMap); err != nil {
		Logger.Debugf("Failed to create ConfigMap %s: %s", OperatorConfigMapTelemetryConfigMapName, err)
		return unknown
	}
	Logger.Debugf("Created ConfigMap %s with UUID %s", OperatorConfigMapTelemetryConfigMapName, newUUID)
	return newUUID
}

func createInitialConfigmap(namespace string) (string, *corev1.ConfigMap) {
	// ConfigMap does not exist or does not contain uuid; create one
	newUUID := uuid.NewString()
	newConfigMap := &corev1.ConfigMap{
		ObjectMeta: v2.ObjectMeta{
			Name:      OperatorConfigMapTelemetryConfigMapName,
			Namespace: namespace,
		},
		Data: map[string]string{
			UUIDKey: newUUID,
		},
	}

	for _, eventType := range AllEventTypes {
		newConfigMap.Data[eventType.GetTimeStampKey()] = TimestampInitialValue
	}

	return newUUID, newConfigMap
}

// isTimestampOlderThanConfiguredFrequency is used to get the timestamp from the ConfigMap and check whether it's time to
// send the data to atlas.
func isTimestampOlderThanConfiguredFrequency(ctx context.Context, k8sClient kubeclient.Client, namespace string, OperatorConfigMapTelemetryConfigMapName string, et EventType) (bool, error) {
	durationStr := envvar.GetEnvOrDefault(SendFrequency, DefaultSendFrequencyStr) // nolint:forbidigo
	duration, err := time.ParseDuration(durationStr)
	if err != nil || duration < 10*time.Minute {
		Logger.Warn("Failed to parse or given durationString: %s too low (min: 10 minutes), defaulting to one week", durationStr)
		duration = DefaultSendFrequency
	}

	cm := &corev1.ConfigMap{}
	err = k8sClient.Get(ctx, types.NamespacedName{Name: OperatorConfigMapTelemetryConfigMapName, Namespace: namespace}, cm)
	if err != nil {
		return false, fmt.Errorf("failed to get ConfigMap: %w", err)
	}
	timestampStr, exists := cm.Data[et.GetTimeStampKey()]

	if !exists {
		return false, fmt.Errorf("timestamp key: %s not found in ConfigMap", et.GetTimeStampKey())
	}

	// We are running this the first time, thus we are "older" than a week
	if timestampStr == TimestampInitialValue {
		return true, nil
	}

	timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
	if err != nil {
		return false, fmt.Errorf("invalid timestamp format: %w", err)
	}

	timestampTime := time.Unix(timestamp, 0)
	cutOffTime := time.Now().Add(-duration)

	isOlder := timestampTime.Before(cutOffTime)

	return isOlder, nil
}

// updateTelemetryConfigMapPayload updates the configmap with the current collected telemetry data
func updateTelemetryConfigMapPayload(ctx context.Context, k8sClient kubeclient.Client, events []Event, namespace string, OperatorConfigMapTelemetryConfigMapName string, eventType EventType) error {
	cm := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: OperatorConfigMapTelemetryConfigMapName, Namespace: namespace}, cm)
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	marshal, err := json.Marshal(events)
	if err != nil {
		return fmt.Errorf("failed to marshal payload: %w", err)
	}

	cm.Data[eventType.GetPayloadKey()] = string(marshal)

	err = k8sClient.Update(ctx, cm)
	if err != nil {
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	return nil
}

// updateTelemetryConfigMapTimeStamp updates the configmap with the current timestamp
func updateTelemetryConfigMapTimeStamp(ctx context.Context, k8sClient kubeclient.Client, namespace string, OperatorConfigMapTelemetryConfigMapName string, eventType EventType) error {
	cm := &corev1.ConfigMap{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: OperatorConfigMapTelemetryConfigMapName, Namespace: namespace}, cm)
	if err != nil {
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	currentTimestamp := strconv.FormatInt(time.Now().Unix(), 10)
	if cm.Data == nil {
		cm.Data = map[string]string{}
	}

	cm.Data[eventType.GetTimeStampKey()] = currentTimestamp

	err = k8sClient.Update(ctx, cm)
	if err != nil {
		return fmt.Errorf("failed to update ConfigMap: %w", err)
	}

	return nil
}
