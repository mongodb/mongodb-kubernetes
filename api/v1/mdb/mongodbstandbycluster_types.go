package mdb

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBStandbyCluster{}, &MongoDBStandbyClusterList{})
}

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path=mongodbstandbyclusters,scope=Namespaced,shortName=mdbstandby
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the standby cluster."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type MongoDBStandbyCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBStandbyClusterStatus `json:"status,omitempty"`
	Spec   MongoDBStandbyClusterSpec   `json:"spec"`
}

type MongoDBStandbyClusterSpec struct {
	// Reference to the existing MongoDB ReplicaSet resource in the same namespace.
	MongoDBResourceRef corev1.LocalObjectReference `json:"mongoDBResourceRef"`

	// OpsManager connection — same ConnectionSpec pattern as MongoDB.
	ConnectionSpec `json:",inline"`

	// Monarch / injector configuration.
	Monarch MonarchSpec `json:"monarch"`

	// Image for the injector sidecar container.
	InjectorImage string `json:"injectorImage"`
}

type MonarchSpec struct {
	// ReplicaSetID is the name of the active replica set (e.g. "activeRS2").
	ReplicaSetID string `json:"replicaSetId"`

	// ClusterPrefix is the Monarch cluster prefix (e.g. "failoverdemo").
	ClusterPrefix string `json:"clusterPrefix"`

	// CredentialsSecretRef references a Secret with keys awsAccessKeyId and awsSecretAccessKey.
	CredentialsSecretRef corev1.LocalObjectReference `json:"credentialsSecretRef"`

	S3BucketName string `json:"s3BucketName"`
	AWSRegion    string `json:"awsRegion"`

	// S3BucketEndpoint is the S3-compatible endpoint URL (e.g. for MinIO).
	// +optional
	S3BucketEndpoint string `json:"s3BucketEndpoint,omitempty"`

	// +optional
	S3PathStyleAccess bool `json:"s3PathStyleAccess,omitempty"`

	// InjectorVersion is the Monarch injector binary version (e.g. "0.1.1").
	InjectorVersion string `json:"injectorVersion"`
}

type MongoDBStandbyClusterStatus struct {
	status.Common `json:",inline"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

func (m *MongoDBStandbyCluster) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	m.Status.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)
	if option, exists := status.GetOption(statusOptions, status.WarningsOption{}); exists {
		m.Status.Warnings = append(m.Status.Warnings, option.(status.WarningsOption).Warnings...)
	}
}

func (m *MongoDBStandbyCluster) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	m.Status.Warnings = warnings
}

func (m *MongoDBStandbyCluster) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m *MongoDBStandbyCluster) GetCommonStatus(options ...status.Option) *status.Common {
	return &m.Status.Common
}

func (m *MongoDBStandbyCluster) GetStatusPath(...status.Option) string {
	return "/status"
}

// project.Reader interface methods — required for ReadConfigAndCredentials.

func (m *MongoDBStandbyCluster) GetProjectConfigMapName() string {
	return m.Spec.GetProject()
}

func (m *MongoDBStandbyCluster) GetProjectConfigMapNamespace() string {
	return m.GetNamespace()
}

func (m *MongoDBStandbyCluster) GetCredentialsSecretName() string {
	return m.Spec.Credentials
}

func (m *MongoDBStandbyCluster) GetCredentialsSecretNamespace() string {
	return m.GetNamespace()
}

// +kubebuilder:object:root=true
type MongoDBStandbyClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MongoDBStandbyCluster `json:"items"`
}
