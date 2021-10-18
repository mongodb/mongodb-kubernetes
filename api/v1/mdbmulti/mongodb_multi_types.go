package mdbmulti

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/10gen/ops-manager-kubernetes/pkg/dns"
	intp "github.com/10gen/ops-manager-kubernetes/pkg/util/int"

	v1 "github.com/10gen/ops-manager-kubernetes/api/v1"
	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/api/v1/status"
	"github.com/10gen/ops-manager-kubernetes/pkg/tls"
	"github.com/blang/semver"
	mdbc "github.com/mongodb/mongodb-kubernetes-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBMulti{}, &MongoDBMultiList{})
}

const (
	LastSuccessfulMultiClusterConfiguration = "mongodb.com/v1.lastSuccessfulConfiguration"
	LastClusterIndexMapping                 = "mongodb.com/v1.lastClusterIndexMapping"
)

// The MongoDBMulti resource allows users to create MongoDB deployment spread over
// multiple clusters

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path= mongodbmulti,scope=Namespaced,shortName=mdbm
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDBMulti resource was created."
type MongoDBMulti struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBMultiStatus `json:"status"`
	Spec   MongoDBMultiSpec   `json:"spec"`
}

func (m MongoDBMulti) GetProjectConfigMapNamespace() string {
	return m.Namespace
}

func (m MongoDBMulti) GetCredentialsSecretNamespace() string {
	return m.Namespace
}

func (m MongoDBMulti) GetProjectConfigMapName() string {
	return m.Spec.OpsManagerConfig.ConfigMapRef.Name
}

func (m MongoDBMulti) GetCredentialsSecretName() string {
	return m.Spec.Credentials
}

func (m MongoDBMulti) GetMultiClusterAgentHostnames() ([]string, error) {
	hostnames := make([]string, 0)

	clusterSpecList, err := m.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	for _, spec := range clusterSpecList {
		hostnames = append(hostnames, dns.GetMultiClusterAgentHostnames(m.Name, m.Namespace, m.ClusterIndex(spec.ClusterName), spec.Members)...)
	}
	return hostnames, nil
}

func (m MongoDBMulti) MultiStatefulsetName(clusterNum int) string {
	return fmt.Sprintf("%s-%d", m.Name, clusterNum)
}

func (m MongoDBMulti) GetBackupSpec() *mdbv1.Backup {
	return m.Spec.Backup
}

func (m MongoDBMulti) GetResourceType() mdbv1.ResourceType {
	return m.Spec.ResourceType
}

func (m MongoDBMulti) GetResourceName() string {
	return m.Name
}

func (m MongoDBMulti) GetHostNameOverrideConfigmapName() string {
	return fmt.Sprintf("%s-hostname-override", m.Name)
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBMultiList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBMulti `json:"items"`
}

func (m MongoDBMulti) GetClusterSpecByName(clusterName string) *ClusterSpecItem {
	for _, csi := range m.Spec.ClusterSpecList.ClusterSpecs {
		if csi.ClusterName == clusterName {
			return &csi
		}
	}
	return nil
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
	Members int `json:"members,omitempty"`
	// +optional
	StatefulSetConfiguration mdbc.StatefulSetConfiguration `json:"statefulSet,omitempty"`
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
	status.Common     `json:",inline"`
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
	ClusterSpecList        ClusterSpecList              `json:"clusterSpecList,omitempty"`
	// Mapping stores the deterministic index for a given cluster-name.
	Mapping map[string]int `json:"-"`
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

func (m *MongoDBMulti) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	m.Status.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BackupStatusOption{}); exists {
		if m.Status.BackupStatus == nil {
			m.Status.BackupStatus = &mdbv1.BackupStatus{}
		}
		m.Status.BackupStatus.StatusName = option.(status.BackupStatusOption).Value().(string)
	}
}

// GetClusterSpecItems returns the cluster spec items that should be used for reconciliation.
// These may not be the values specified in the spec directly, scaling of both clusters and replica set members taken
// into account. The return value should be used in the reconciliation loop when determining which processes
// should be added to the automation config and which services need to be created and how many replicas
// each StatefulSet should have.
// This function should always be used instead of accessing the struct fields directly in the Reconcile function.
func (m *MongoDBMulti) GetClusterSpecItems() ([]ClusterSpecItem, error) {
	clusterSpecs := m.Spec.GetOrderedClusterSpecList()
	prevSpec, err := m.ReadLastAchievedSpec()
	if err != nil {
		return nil, err
	}

	if prevSpec == nil {
		return clusterSpecs, nil
	}

	prevSpecs := prevSpec.GetOrderedClusterSpecList()

	var specsForThisReconciliation []ClusterSpecItem
	specsForThisReconciliation = append(specsForThisReconciliation, prevSpecs...)

	// When we remove a cluster, this means that there will be an entry in the resource annotation (the previous spec)
	// but not in the current spec. In order to make scaling work, we add an entry for the removed cluster that has
	// 0 members. This allows the following scaling logic to handle the transition from n -> 0 members, with a
	// decrementing value of one with each reconciliation. After this, we delete the StatefulSet if the spec item
	// was removed.

	// E.g.
	// Reconciliation 1:
	//    3 clusters all with 3 members
	// Reconciliation 2:
	//    2 clusters with 3 members (we removed the last cluster.
	//    The spec has 2 members, but we add a third with 0 members.
	//    This "dummy" item will be handled the same as another spec item.
	//    This is only relevant for the first reconciliation after removal since this cluster spec will be saved
	//    in an annotation, and the regular scaling logic will happen in subsequent reconciliations.
	//    We go from members 3-3-3 to 3-3-2
	// Reconciliation 3:
	//   We go from 3-3-2 to 3-3-1
	// Reconciliation 4:
	//   We go from 3-3-1 to 3-3-0 (and then delete the StatefulSet in this final reconciliation)

	clusterSpecsMap := clusterSpecItemListToMap(clusterSpecs)
	for _, previousItem := range prevSpecs {
		if _, ok := clusterSpecsMap[previousItem.ClusterName]; !ok {
			previousItem.Members = 0
			clusterSpecs = append(clusterSpecs, previousItem)
		}
	}

	prevSpecsMap := clusterSpecItemListToMap(prevSpecs)
	for _, item := range clusterSpecs {
		// if a spec item exists but was not there previously, we add it with a single member.
		// this allows subsequent reconciliations to go from 1-> n  one member at a time as usual.
		// it will never be possible to add a new member at the maximum members since scaling can only ever be done
		// one at a time. Adding the item with 1 member allows the regular logic to handle scaling one a time until
		// we reach the desired member count.
		prevItem, ok := prevSpecsMap[item.ClusterName]
		if !ok {
			if item.Members > 1 {
				item.Members = 1
			}
			return append(specsForThisReconciliation, item), nil
		}

		// can only scale one member at a time so we return early on each increment.
		if item.Members > prevItem.Members {
			specsForThisReconciliation[m.ClusterIndex(item.ClusterName)].Members += 1
			return specsForThisReconciliation, nil
		}
		if item.Members < prevItem.Members {
			specsForThisReconciliation[m.ClusterIndex(item.ClusterName)].Members -= 1
			return specsForThisReconciliation, nil
		}
	}

	return specsForThisReconciliation, nil
}

// clusterSpecItemListToMap converts a slice of cluster spec items into a map using the name as the key.
func clusterSpecItemListToMap(clusterSpecItems []ClusterSpecItem) map[string]ClusterSpecItem {
	m := map[string]ClusterSpecItem{}
	for _, c := range clusterSpecItems {
		m[c.ClusterName] = c
	}
	return m
}

// ReadLastAchievedSpec fetches the previously achieved spec.
func (m *MongoDBMulti) ReadLastAchievedSpec() (*MongoDBMultiSpec, error) {
	if m.Annotations == nil {
		return nil, nil
	}
	specBytes, ok := m.Annotations[LastSuccessfulMultiClusterConfiguration]
	if !ok {
		return nil, nil
	}

	prevSpec := &MongoDBMultiSpec{}
	if err := json.Unmarshal([]byte(specBytes), &prevSpec); err != nil {
		return nil, err
	}
	return prevSpec, nil
}

// when unmarshaling a MongoDBMulti instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references
func (m *MongoDBMulti) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBMulti
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	m.InitDefaults()
	return nil
}

// InitDefaults makes sure the MongoDBMulti resource has correct state after initialization:
// - prevents any references from having nil values.
// - makes sure the spec is in correct state
//
// should not be called directly, used in tests and unmarshalling
func (m *MongoDBMulti) InitDefaults() {
	m.Spec.Security = mdbv1.EnsureSecurity(m.Spec.Security)

	// TODO: add more default if need be
	// ProjectName defaults to the name of the resource
	if m.Spec.ProjectName == "" {
		m.Spec.ProjectName = m.Name
	}

	if m.Spec.Agent.StartupParameters == nil {
		m.Spec.Agent.StartupParameters = map[string]string{}
	}

	if m.Spec.AdditionalMongodConfig.Object == nil {
		m.Spec.AdditionalMongodConfig.Object = map[string]interface{}{}
	}
}

// Replicas returns the total number of MongoDB members running across all the clusters
func (m *MongoDBMultiSpec) Replicas() int {
	num := 0
	for _, e := range m.ClusterSpecList.ClusterSpecs {
		num += e.Members
	}
	return num
}

func (m *MongoDBMultiSpec) GetClusterDomain() string {
	return m.ClusterDomain
}

func (m *MongoDBMultiSpec) GetMongoDBVersion() string {
	return m.Version
}

func (m *MongoDBMultiSpec) GetSecurity() *mdbv1.Security {
	if m.Security == nil {
		return &mdbv1.Security{}
	}
	return m.Security
}
func (m *MongoDBMultiSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (m *MongoDBMultiSpec) GetResourceType() mdbv1.ResourceType {
	return m.ResourceType
}

func (m *MongoDBMultiSpec) IsSecurityTLSConfigEnabled() bool {
	return m.GetSecurity().TLSConfig.IsEnabled()
}

func (m *MongoDBMultiSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func (m *MongoDBMultiSpec) GetTLSMode() tls.Mode {
	if m.Security == nil || !m.Security.TLSConfig.IsEnabled() {
		return tls.Disabled
	}

	return tls.GetTLSModeFromMongodConfig(m.AdditionalMongodConfig.Object)
}

func (m *MongoDBMultiSpec) GetHorizonConfig() []mdbv1.MongoDBHorizonConfig {
	return m.Connectivity.ReplicaSetHorizons
}

func (m *MongoDBMultiSpec) GetAdditionalMongodConfig() mdbv1.AdditionalMongodConfig {
	return m.AdditionalMongodConfig
}

func (m *MongoDBMultiSpec) MinimumMajorVersion() uint64 {
	if m.FeatureCompatibilityVersion != nil && *m.FeatureCompatibilityVersion != "" {
		fcv := *m.FeatureCompatibilityVersion

		// ignore errors here as the format of FCV/version is handled by CRD validation
		semverFcv, _ := semver.Make(fmt.Sprintf("%s.0", fcv))
		return semverFcv.Major
	}
	semverVersion, _ := semver.Make(m.GetMongoDBVersion())
	return semverVersion.Major
}

// GetOrderedClusterSpecList returns the cluster spec items sorted by name.
func (m *MongoDBMultiSpec) GetOrderedClusterSpecList() []ClusterSpecItem {
	clusterSpecs := m.ClusterSpecList.ClusterSpecs
	sort.SliceStable(clusterSpecs, func(i, j int) bool {
		return clusterSpecs[i].ClusterName < clusterSpecs[j].ClusterName
	})
	return clusterSpecs
}

// ClusterIndex returns the index associated with a given clusterName, it assigns a unique id to each
// clustername taking into account addition and removal of clusters. We don't reuse cluster indexes since
// the clusters can be removed and then added back.
func (m *MongoDBMulti) ClusterIndex(clusterName string) int {
	if m.Spec.Mapping == nil {
		m.Spec.Mapping = make(map[string]int)
	}
	// first check if the entry exists in local map before making any API call
	if val, ok := m.Spec.Mapping[clusterName]; ok {
		return val
	}

	// next check if the clusterName is present in the annotations
	if bytes, ok := m.Annotations[LastClusterIndexMapping]; ok {
		json.Unmarshal([]byte(bytes), &m.Spec.Mapping)

		if val, ok := m.Spec.Mapping[clusterName]; ok {
			return val
		}
	}

	index := getNextIndex(m.Spec.Mapping)
	m.Spec.Mapping[clusterName] = index
	return index
}

// getNextIndex returns the next higher index from the current cluster indexes
func getNextIndex(m map[string]int) int {
	maxi := -1

	for _, val := range m {
		maxi = intp.Max(maxi, val)
	}
	return maxi + 1
}
