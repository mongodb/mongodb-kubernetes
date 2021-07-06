package mdbmulti

import (
	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBMulti{}, &MongoDBMultiList{})
}

// The MongoDBMulti resource allows users to create MongoDB deployment spread over
// multiple clusters

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:resource:path= mongodbmulti,scope=Namespaced,shortName=mdbm
// +kubebuilder:subresource:status
type MongoDBMulti struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBMultiStatus `json:"status"`
	Spec   MongoDBMultiSpec   `json:"spec"`
}

func (m MongoDBMulti) GetProjectConfigMapNamespace() string {
	return m.Spec.Namespace
}

func (m MongoDBMulti) GetCredentialsSecretNamespace() string {
	return m.Spec.Namespace
}

func (m MongoDBMulti) GetProjectConfigMapName() string {
	return m.Spec.OpsManagerConfig.ConfigMapRef.Name
}

func (m MongoDBMulti) GetCredentialsSecretName() string {
	return m.Spec.Credentials
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBMultiList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBMulti `json:"items"`
}

// ClusterSpecList holds a list with a clusterSpec corresponding to each cluster
type ClusterSpecList struct {
	ClusterSpecs []ClusterSpecItem `json:"clusterSpecs,omitempty"`
}

// ClusterSpecItem is the mongodb multi-cluster spec that is specific to a
// particular Kubernetes cluster, this maps to the statefulset created in each cluster
type ClusterSpecItem struct {
	// ClusterName is name of the cluster where the MongoDB Statefulset will be scheduled, the
	// name should have a one on one mapping with the service-account created in the central cluster
	// to talk to the workload clusters.
	ClusterName string `json:"clusterName,omitempty"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service string `json:"service,omitempty"`
	// ExposedExternally determines whether a NodePort service should be created for the resource
	ExposedExternally bool `json:"exposedExternally,omitempty"`
	// Amount of members for this MongoDB Replica Set
	Members int                   `json:"members,omitempty"`
	PodSpec *mdbv1.MongoDbPodSpec `json:"podSpec,omitempty"`
}

// ClusterStatusList holds a list of clusterStatuses corresponding to each cluster
type ClusterStatusList struct {
	ClusterStatuses []ClusterStatusItem `json:"clusterStatuses,omitempty"`
}

// ClusterStatusItem is the mongodb multi-cluster spec that is specific to a
// particular Kubernetes cluster, this maps to the statefulset created in each cluster
type ClusterStatusItem struct {
	// ClusterName is name of the cluster where the MongoDB Statefulset will be scheduled, the
	// name should have a one on one mapping with the service-account created in the central cluster
	// to talk to the workload clusters.
	ClusterName   string `json:"clusterName,omitempty"`
	status.Common `json:",inline"`
	Members       int              `json:"members,omitempty"`
	Warnings      []status.Warning `json:"warnings,omitempty"`
}

type MongoDBMultiStatus struct {
	ClusterStatusList ClusterStatusList   `json:"clusterStatusList,omitempty"`
	BackupStatus      *mdbv1.BackupStatus `json:"backup,omitempty"`
	Version           string              `json:"version"`
	Link              string              `json:"link,omitempty"`
	Warnings          []status.Warning    `json:"warnings,omitempty"`
}

type MongoDBMultiSpec struct {
	// +kubebuilder:validation:Pattern=^[0-9]+.[0-9]+.[0-9]+(-.+)?$|^$
	// +kubebuilder:validation:Required
	Version                     string            `json:"version"`
	FeatureCompatibilityVersion *string           `json:"featureCompatibilityVersion,omitempty"`
	Agent                       mdbv1.AgentConfig `json:"agent,omitempty"`
	// +kubebuilder:validation:Format="hostname"
	ClusterDomain        string `json:"clusterDomain,omitempty"`
	mdbv1.ConnectionSpec `json:",inline"`
	Persistent           *bool `json:"persistent,omitempty"`
	// In few service mesh options for ex: Istio, by default we would need to duplicate the
	// service objects created per pod in all the clusters to enable DNS resolution. Users can
	// however configure their ServiceMesh with DNS proxy(https://istio.io/latest/docs/ops/configuration/traffic-management/dns-proxy/)
	// enabled in which case the operator doesn't need to create the service objects per cluster. This options tells the operator
	// whether it should create the service objects in all the clusters or not. By default, if not specified the operator would create the duplicate svc objects.
	DuplicateServiceObjects *bool `json:"duplicateServiceObjects,omitempty"`
	// +kubebuilder:validation:Enum=ReplicaSet
	// +kubebuilder:validation:Required
	ResourceType mdbv1.ResourceType `json:"type"`
	// +optional
	Security     *mdbv1.Security            `json:"security,omitempty"`
	Connectivity *mdbv1.MongoDBConnectivity `json:"connectivity,omitempty"`
	Backup       *mdbv1.Backup              `json:"backup,omitempty"`

	// AdditionalMongodConfig is additional configuration that can be passed to
	// each data-bearing mongod at runtime. Uses the same structure as the mongod
	// configuration file:
	// https://docs.mongodb.com/manual/reference/configuration-options/
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	AdditionalMongodConfig mdbv1.AdditionalMongodConfig `json:"additionalMongodConfig,omitempty"`

	// Namespace is the namespace that the created resources are deployed to in the member clusters.
	Namespace       string          `json:"namespace"`
	ClusterSpecList ClusterSpecList `json:"clusterSpecList,omitempty"`

	OpsManagerConfig mdbv1.PrivateCloudConfig `json:"opsManager"`
}

func (m MongoDBMulti) GetPlural() string {
	return "mongodbmulti"
}

func (m *MongoDBMulti) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m *MongoDBMulti) GetStatusPath(...status.Option) string {
	return "/status"
}

func (m *MongoDBMulti) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	m.Status.Warnings = warnings
}

func (m *MongoDBMulti) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {}
