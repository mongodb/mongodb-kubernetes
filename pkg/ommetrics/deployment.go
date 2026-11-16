package ommetrics

import (
	"encoding/json"
	"errors"
	"reflect"

	"go.opentelemetry.io/otel/attribute"
)

const deploymentMappingsAttributeKey = "mongodb.opsmanager.mck.deployment.mappings"

// MongoDBDeployment is a replica set, sharded cluster or standalone tracked by Ops Manager (not a Kubernetes Deployment).
type MongoDBDeployment interface {
	TelemetryIdentifier() string
	OpsManagerDeploymentName() string
}

type mckDeploymentMapping struct {
	MCKDeploymentUID string `json:"mckDeploymentUid"`
	DeploymentName   string `json:"deploymentName"`
}

func deploymentMappingsAttribute(deployment MongoDBDeployment) (attribute.KeyValue, error) {
	if v := reflect.ValueOf(deployment); !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return attribute.KeyValue{}, errors.New("deployment is required")
	}
	m := mckDeploymentMapping{MCKDeploymentUID: deployment.TelemetryIdentifier(), DeploymentName: deployment.OpsManagerDeploymentName()}
	if m.MCKDeploymentUID == "" || m.DeploymentName == "" {
		return attribute.KeyValue{}, errors.New("deployment telemetry identifier and automation deployment name are required")
	}
	b, err := json.Marshal([]mckDeploymentMapping{m})
	if err != nil {
		return attribute.KeyValue{}, err
	}
	return attribute.String(deploymentMappingsAttributeKey, string(b)), nil
}
