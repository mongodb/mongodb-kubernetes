package mdbmulti

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/blang/semver"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/mongodb/mongodb-kubernetes/api/v1"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/status"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/connectionstring"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/ldap"
	mdbc "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/fcv"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster/failedcluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	intp "github.com/mongodb/mongodb-kubernetes/pkg/util/int"
)

func init() {
	v1.SchemeBuilder.Register(&MongoDBMultiCluster{}, &MongoDBMultiClusterList{})
}

type TransportSecurity string

const (
	LastClusterNumMapping                   = "mongodb.com/v1.lastClusterNumMapping"
	TransportSecurityNone TransportSecurity = "none"
	TransportSecurityTLS  TransportSecurity = "tls"

	LabelResourceOwner = "mongodbmulticluster"
)

// The MongoDBMultiCluster resource allows users to create MongoDB deployment spread over
// multiple clusters

// +kubebuilder:object:root=true
// +k8s:openapi-gen=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:path= mongodbmulticluster,scope=Namespaced,shortName=mdbmc
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase",description="Current state of the MongoDB deployment."
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="The time since the MongoDBMultiCluster resource was created."
type MongoDBMultiCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// +optional
	Status MongoDBMultiStatus `json:"status"`
	Spec   MongoDBMultiSpec   `json:"spec"`
}

func (m *MongoDBMultiCluster) GetProjectConfigMapNamespace() string {
	return m.Namespace
}

func (m *MongoDBMultiCluster) GetCredentialsSecretNamespace() string {
	return m.Namespace
}

func (m *MongoDBMultiCluster) GetProjectConfigMapName() string {
	return m.Spec.OpsManagerConfig.ConfigMapRef.Name
}

func (m *MongoDBMultiCluster) GetCredentialsSecretName() string {
	return m.Spec.Credentials
}

func (m *MongoDBMultiCluster) GetMultiClusterAgentHostnames() ([]string, error) {
	hostnames := make([]string, 0)

	clusterSpecList, err := m.GetClusterSpecItems()
	if err != nil {
		return nil, err
	}

	for _, spec := range clusterSpecList {
		hostnames = append(hostnames, dns.GetMultiClusterProcessHostnames(m.Name, m.Namespace, m.ClusterNum(spec.ClusterName), spec.Members, m.Spec.GetClusterDomain(), nil)...)
	}
	return hostnames, nil
}

func (m *MongoDBMultiCluster) MultiStatefulsetName(clusterNum int) string {
	return dns.GetMultiStatefulSetName(m.Name, clusterNum)
}

func (m *MongoDBMultiCluster) MultiHeadlessServiceName(clusterNum int) string {
	return fmt.Sprintf("%s-svc", m.MultiStatefulsetName(clusterNum))
}

func (m *MongoDBMultiCluster) GetBackupSpec() *mdbv1.Backup {
	return m.Spec.Backup
}

func (m *MongoDBMultiCluster) GetResourceType() mdbv1.ResourceType {
	return m.Spec.ResourceType
}

func (m *MongoDBMultiCluster) GetResourceName() string {
	return m.Name
}

func (m *MongoDBMultiCluster) GetSecurity() *mdbv1.Security {
	return m.Spec.Security
}

func (m *MongoDBMultiCluster) GetConnectionSpec() *mdbv1.ConnectionSpec {
	return &m.Spec.ConnectionSpec
}

func (m *MongoDBMultiCluster) GetPrometheus() *mdbc.Prometheus {
	return m.Spec.Prometheus
}

func (m *MongoDBMultiCluster) IsLDAPEnabled() bool {
	if m.Spec.Security == nil || m.Spec.Security.Authentication == nil {
		return false
	}
	return m.Spec.Security.Authentication.IsLDAPEnabled()
}

func (m *MongoDBMultiCluster) IsOIDCEnabled() bool {
	if m.Spec.Security == nil || m.Spec.Security.Authentication == nil {
		return false
	}
	return m.Spec.Security.Authentication.IsOIDCEnabled()
}

func (m *MongoDBMultiCluster) GetLDAP(password, caContents string) *ldap.Ldap {
	if !m.IsLDAPEnabled() {
		return nil
	}
	mdbLdap := m.Spec.Security.Authentication.Ldap
	transportSecurity := mdbv1.GetTransportSecurity(mdbLdap)

	validateServerConfig := true
	if mdbLdap.ValidateLDAPServerConfig != nil {
		validateServerConfig = *mdbLdap.ValidateLDAPServerConfig
	}

	return &ldap.Ldap{
		BindQueryUser:            mdbLdap.BindQueryUser,
		BindQueryPassword:        password,
		Servers:                  strings.Join(mdbLdap.Servers, ","),
		TransportSecurity:        string(transportSecurity),
		CaFileContents:           caContents,
		ValidateLDAPServerConfig: validateServerConfig,

		// Related to LDAP Authorization
		AuthzQueryTemplate: mdbLdap.AuthzQueryTemplate,
		UserToDnMapping:    mdbLdap.UserToDNMapping,

		// TODO: Enable LDAP SASL bind method
		BindMethod:         "simple",
		BindSaslMechanisms: "",

		TimeoutMS:                     mdbLdap.TimeoutMS,
		UserCacheInvalidationInterval: mdbLdap.UserCacheInvalidationInterval,
	}
}

func (m *MongoDBMultiCluster) GetHostNameOverrideConfigmapName() string {
	return fmt.Sprintf("%s-hostname-override", m.Name)
}

func (m *MongoDBMultiCluster) ObjectKey() client.ObjectKey {
	return kube.ObjectKey(m.Namespace, m.Name)
}

func (m *MongoDBMultiCluster) GetOwnerLabels() map[string]string {
	return map[string]string{
		util.OperatorLabelName: util.OperatorLabelValue,
		LabelResourceOwner:     fmt.Sprintf("%s-%s", m.Namespace, m.Name),
	}
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
type MongoDBMultiClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDBMultiCluster `json:"items"`
}

func (m *MongoDBMultiCluster) GetClusterSpecByName(clusterName string) *mdbv1.ClusterSpecItem {
	for _, csi := range m.Spec.ClusterSpecList {
		if csi.ClusterName == clusterName {
			return &csi
		}
	}
	return nil
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
	status.Common               `json:",inline"`
	ClusterStatusList           ClusterStatusList   `json:"clusterStatusList,omitempty"`
	BackupStatus                *mdbv1.BackupStatus `json:"backup,omitempty"`
	Version                     string              `json:"version"`
	Link                        string              `json:"link,omitempty"`
	FeatureCompatibilityVersion string              `json:"featureCompatibilityVersion,omitempty"`
	Warnings                    []status.Warning    `json:"warnings,omitempty"`
}

type MongoDBMultiSpec struct {
	// +kubebuilder:pruning:PreserveUnknownFields
	mdbv1.DbCommonSpec `json:",inline"`

	ClusterSpecList mdbv1.ClusterSpecList `json:"clusterSpecList,omitempty"`

	// Mapping stores the deterministic index for a given cluster-name.
	Mapping map[string]int `json:"-"`
}

func (m *MongoDBMultiSpec) GetAgentConfig() mdbv1.AgentConfig {
	return m.Agent
}

func (m *MongoDBMultiCluster) GetStatus(...status.Option) interface{} {
	return m.Status
}

func (m *MongoDBMultiCluster) GetCommonStatus(...status.Option) *status.Common {
	return &m.Status.Common
}

func (m *MongoDBMultiCluster) GetStatusPath(...status.Option) string {
	return "/status"
}

func (m *MongoDBMultiCluster) SetWarnings(warnings []status.Warning, _ ...status.Option) {
	m.Status.Warnings = warnings
}

func (m *MongoDBMultiCluster) UpdateStatus(phase status.Phase, statusOptions ...status.Option) {
	m.Status.UpdateCommonFields(phase, m.GetGeneration(), statusOptions...)

	if option, exists := status.GetOption(statusOptions, status.BackupStatusOption{}); exists {
		if m.Status.BackupStatus == nil {
			m.Status.BackupStatus = &mdbv1.BackupStatus{}
		}
		m.Status.BackupStatus.StatusName = option.(status.BackupStatusOption).Value().(string)
	}

	if phase == status.PhaseRunning {
		m.Status.FeatureCompatibilityVersion = m.CalculateFeatureCompatibilityVersion()
	}
}

// GetClusterSpecItems returns the cluster spec items that should be used for reconciliation.
// These may not be the values specified in the spec directly, this takes into account the following three conditions:
// 1. Adding/Removing cluster from the clusterSpecList.
// 2. Scaling the number of nodes of each cluster.
// 3. When there is a cluster outage, there is an annotation put in the CR to orchestrate workloads out
// of the impacted cluster to the remaining clusters.
// The return value should be used in the reconciliation loop when determining which processes
// should be added to the automation config and which services need to be created and how many replicas
// each StatefulSet should have.
// This function should always be used instead of accessing the struct fields directly in the Reconcile function.
func (m *MongoDBMultiCluster) GetClusterSpecItems() (mdbv1.ClusterSpecList, error) {
	desiredSpecList := m.GetDesiredSpecList()
	prevSpec, err := m.ReadLastAchievedSpec()
	if err != nil {
		return nil, err
	}

	if prevSpec == nil {
		return desiredSpecList, nil
	}

	prevSpecs := prevSpec.GetClusterSpecList()

	desiredSpecMap := clusterSpecItemListToMap(desiredSpecList)
	prevSpecsMap := clusterSpecItemListToMap(prevSpecs)

	var specsForThisReconciliation mdbv1.ClusterSpecList

	// We only care about the members of the previous reconcile, the rest should be reflecting the CRD definition.
	for _, spec := range prevSpecs {
		if desiredSpec, ok := desiredSpecMap[spec.ClusterName]; ok {
			prevMembers := spec.Members
			spec = desiredSpec
			spec.Members = prevMembers
		}
		specsForThisReconciliation = append(specsForThisReconciliation, spec)
	}

	// When we remove a cluster, this means that there will be an entry in the resource annotation (the previous spec)
	// but not in the current spec. In order to make scaling work, we add an entry for the removed cluster that has
	// 0 members. This allows the following scaling down logic to handle the transition from n -> 0 members, with a
	// decrementing value of one with each reconciliation. After this, we delete the StatefulSet if the spec item
	// was removed.

	// E.g.
	// Reconciliation 1:
	//    3 clusters all with 3 members
	// Reconciliation 2:
	//    2 clusters with 3 members (we removed the last cluster).
	//    The spec has 2 members, but we add a third with 0 members.
	//    This "dummy" item will be handled the same as another spec item.
	//    This is only relevant for the first reconciliation after removal since this cluster spec will be saved
	//    in an annotation, and the regular scaling logic will happen in subsequent reconciliations.
	//    We go from members 3-3-3 to 3-3-2
	// Reconciliation 3:
	//   We go from 3-3-2 to 3-3-1
	// Reconciliation 4:
	//   We go from 3-3-1 to 3-3-0 (and then delete the StatefulSet in this final reconciliation)

	for _, previousItem := range prevSpecs {
		if _, ok := desiredSpecMap[previousItem.ClusterName]; !ok {
			previousItem.Members = 0
			desiredSpecList = append(desiredSpecList, previousItem)
		}
	}

	for _, item := range desiredSpecList {
		// If a spec item exists but was not there previously, we add it with a single member.
		// This allows subsequent reconciliations to go from 1-> n one member at a time as usual.
		// It will never be possible to add a new member at the maximum members since scaling can only ever be done
		// one at a time. Adding the item with 1 member allows the regular logic to handle scaling one a time until
		// we reach the desired member count.
		prevItem, ok := prevSpecsMap[item.ClusterName]
		if !ok {
			if item.Members > 1 {
				item.Members = 1
			}
			return append(specsForThisReconciliation, item), nil
		}
		// can only scale one member at a time, so we return early on each increment.
		if item.Members > prevItem.Members {
			specsForThisReconciliation[m.ClusterNum(item.ClusterName)].Members += 1
			return specsForThisReconciliation, nil
		}
		if item.Members < prevItem.Members {
			specsForThisReconciliation[m.ClusterNum(item.ClusterName)].Members -= 1
			return specsForThisReconciliation, nil
		}
	}

	return specsForThisReconciliation, nil
}

// HasClustersToFailOver checks if the MongoDBMultiCluster CR has ""clusterSpecOverride" annotation which is put when one or more clusters
// are not reachable.
func HasClustersToFailOver(annotations map[string]string) (string, bool) {
	if annotations == nil {
		return "", false
	}
	val, ok := annotations[failedcluster.ClusterSpecOverrideAnnotation]
	return val, ok
}

// GetFailedClusters returns the current set of failed clusters for the MongoDBMultiCluster CR.
func (m *MongoDBMultiCluster) GetFailedClusters() ([]failedcluster.FailedCluster, error) {
	if m.Annotations == nil {
		return nil, nil
	}
	failedClusterBytes, ok := m.Annotations[failedcluster.FailedClusterAnnotation]
	if !ok {
		return []failedcluster.FailedCluster{}, nil
	}
	var failedClusters []failedcluster.FailedCluster
	err := json.Unmarshal([]byte(failedClusterBytes), &failedClusters)
	if err != nil {
		return nil, err
	}
	return failedClusters, err
}

// GetFailedClusterNames returns the current set of failed cluster names for the MongoDBMultiCluster CR.
func (m *MongoDBMultiCluster) GetFailedClusterNames() ([]string, error) {
	failedClusters, err := m.GetFailedClusters()
	if err != nil {
		return nil, err
	}
	clusterNames := []string{}
	for _, c := range failedClusters {
		clusterNames = append(clusterNames, c.ClusterName)
	}
	return clusterNames, nil
}

// clusterSpecItemListToMap converts a slice of cluster spec items into a map using the name as the key.
func clusterSpecItemListToMap(clusterSpecItems mdbv1.ClusterSpecList) map[string]mdbv1.ClusterSpecItem {
	m := map[string]mdbv1.ClusterSpecItem{}
	for _, c := range clusterSpecItems {
		m[c.ClusterName] = c
	}
	return m
}

// ReadLastAchievedSpec fetches the previously achieved spec.
func (m *MongoDBMultiCluster) ReadLastAchievedSpec() (*MongoDBMultiSpec, error) {
	if m.Annotations == nil {
		return nil, nil
	}
	specBytes, ok := m.Annotations[util.LastAchievedSpec]
	if !ok {
		return nil, nil
	}

	prevSpec := &MongoDBMultiSpec{}
	if err := json.Unmarshal([]byte(specBytes), &prevSpec); err != nil {
		return nil, err
	}
	return prevSpec, nil
}

func (m *MongoDBMultiCluster) GetLastAdditionalMongodConfig() map[string]interface{} {
	lastSpec, err := m.ReadLastAchievedSpec()
	if lastSpec == nil || err != nil {
		return map[string]interface{}{}
	}
	return lastSpec.GetAdditionalMongodConfig().ToMap()
}

// when unmarshalling a MongoDBMultiCluster instance, we don't want to have any nil references
// these are replaced with an empty instance to prevent nil references
func (m *MongoDBMultiCluster) UnmarshalJSON(data []byte) error {
	type MongoDBJSON *MongoDBMultiCluster
	if err := json.Unmarshal(data, (MongoDBJSON)(m)); err != nil {
		return err
	}

	m.InitDefaults()
	return nil
}

// InitDefaults makes sure the MongoDBMultiCluster resource has correct state after initialization:
// - prevents any references from having nil values.
// - makes sure the spec is in correct state
//
// should not be called directly, used in tests and unmarshalling
func (m *MongoDBMultiCluster) InitDefaults() {
	m.Spec.Security = mdbv1.EnsureSecurity(m.Spec.Security)

	// TODO: add more default if need be
	// ProjectName defaults to the name of the resource
	if m.Spec.ProjectName == "" {
		m.Spec.ProjectName = m.Name
	}

	if m.Spec.Agent.StartupParameters == nil {
		m.Spec.Agent.StartupParameters = map[string]string{}
	}

	if m.Spec.AdditionalMongodConfig == nil || m.Spec.AdditionalMongodConfig.ToMap() == nil {
		m.Spec.AdditionalMongodConfig = &mdbv1.AdditionalMongodConfig{}
	}

	if m.Spec.CloudManagerConfig == nil {
		m.Spec.CloudManagerConfig = mdbv1.NewOpsManagerConfig()
	}

	if m.Spec.OpsManagerConfig == nil {
		m.Spec.OpsManagerConfig = mdbv1.NewOpsManagerConfig()
	}

	if m.Spec.Connectivity == nil {
		m.Spec.Connectivity = mdbv1.NewConnectivity()
	}

	m.Spec.Security = mdbv1.EnsureSecurity(m.Spec.Security)
}

// Replicas returns the total number of MongoDB members running across all the clusters
func (m *MongoDBMultiSpec) Replicas() int {
	num := 0
	for _, e := range m.ClusterSpecList {
		num += e.Members
	}
	return num
}

func (m *MongoDBMultiSpec) GetClusterDomain() string {
	if m.ClusterDomain != "" {
		return m.ClusterDomain
	}
	return "cluster.local"
}

func (m *MongoDBMultiSpec) GetMongoDBVersion() string {
	return m.Version
}

func (m *MongoDBMultiSpec) GetSecurityAuthenticationModes() []string {
	return m.GetSecurity().Authentication.GetModes()
}

func (m *MongoDBMultiSpec) GetResourceType() mdbv1.ResourceType {
	return m.ResourceType
}

func (m *MongoDBMultiSpec) IsSecurityTLSConfigEnabled() bool {
	return m.GetSecurity().IsTLSEnabled()
}

func (m *MongoDBMultiSpec) GetFeatureCompatibilityVersion() *string {
	return m.FeatureCompatibilityVersion
}

func (m *MongoDBMultiSpec) GetHorizonConfig() []mdbv1.MongoDBHorizonConfig {
	return m.Connectivity.ReplicaSetHorizons
}

func (m *MongoDBMultiSpec) GetMemberOptions() []automationconfig.MemberOptions {
	specList := m.GetClusterSpecList()
	var options []automationconfig.MemberOptions
	for _, item := range specList {
		options = append(options, item.MemberConfig...)
	}
	return options
}

func (m *MongoDBMultiSpec) MinimumMajorVersion() uint64 {
	if m.FeatureCompatibilityVersion != nil && *m.FeatureCompatibilityVersion != "" {
		fcvString := *m.FeatureCompatibilityVersion

		// ignore errors here as the format of FCV/version is handled by CRD validation
		semverFcv, _ := fcv.FeatureCompatibilityVersionToSemverFormat(fcvString)
		return semverFcv.Major
	}
	semverVersion, _ := semver.Make(m.GetMongoDBVersion())
	return semverVersion.Major
}

func (m *MongoDBMultiSpec) GetPersistence() bool {
	if m.Persistent == nil {
		return true
	}
	return *m.Persistent
}

// GetClusterSpecList returns the cluster spec items.
// This method should ideally be not used in the reconciler. Always, prefer to
// use the GetHealthyMemberClusters() method from the reconciler.
func (m *MongoDBMultiSpec) GetClusterSpecList() mdbv1.ClusterSpecList {
	return m.ClusterSpecList
}

func (m *MongoDBMultiSpec) GetExternalAccessConfigurationForMemberCluster(clusterName string) *mdbv1.ExternalAccessConfiguration {
	for _, csl := range m.ClusterSpecList {
		if csl.ClusterName == clusterName && csl.ExternalAccessConfiguration != nil {
			return csl.ExternalAccessConfiguration
		}
	}

	return m.ExternalAccessConfiguration
}

func (m *MongoDBMultiSpec) GetExternalDomainForMemberCluster(clusterName string) *string {
	if cfg := m.GetExternalAccessConfigurationForMemberCluster(clusterName); cfg != nil {
		if externalDomain := cfg.ExternalDomain; externalDomain != nil {
			return externalDomain
		}
	}

	return m.GetExternalDomain()
}

// GetDesiredSpecList returns the desired cluster spec list for a given reconcile operation.
// Returns the failerOver annotation if present else reads the cluster spec list from the CR.
func (m *MongoDBMultiCluster) GetDesiredSpecList() mdbv1.ClusterSpecList {
	clusterSpecList := m.Spec.ClusterSpecList

	if val, ok := HasClustersToFailOver(m.GetAnnotations()); ok {
		var clusterSpecOverride mdbv1.ClusterSpecList

		err := json.Unmarshal([]byte(val), &clusterSpecOverride)
		if err != nil {
			return clusterSpecList
		}
		clusterSpecList = clusterSpecOverride
	}
	return clusterSpecList
}

// ClusterNum returns the index associated with a given clusterName, it assigns a unique id to each
// clustername taking into account addition and removal of clusters. We don't reuse cluster indexes since
// the clusters can be removed and then added back.
func (m *MongoDBMultiCluster) ClusterNum(clusterName string) int {
	if m.Spec.Mapping == nil {
		m.Spec.Mapping = make(map[string]int)
	}
	// first check if the entry exists in local map before making any API call
	if val, ok := m.Spec.Mapping[clusterName]; ok {
		return val
	}

	// next check if the clusterName is present in the annotations
	if bytes, ok := m.Annotations[LastClusterNumMapping]; ok {
		_ = json.Unmarshal([]byte(bytes), &m.Spec.Mapping)

		if val, ok := m.Spec.Mapping[clusterName]; ok {
			return val
		}
	}

	index := getNextIndex(m.Spec.Mapping)
	m.Spec.Mapping[clusterName] = index
	return index
}

// BuildConnectionString for a MultiCluster user.
//
// Not yet functional, because m.Service() is not defined. Waiting for CLOUDP-105817
// to complete.
func (m *MongoDBMultiCluster) BuildConnectionString(username, password string, scheme connectionstring.Scheme, connectionParams map[string]string) string {
	hostnames := make([]string, 0)
	for _, spec := range m.Spec.GetClusterSpecList() {
		hostnames = append(hostnames, dns.GetMultiClusterProcessHostnames(m.Name, m.Namespace, m.ClusterNum(spec.ClusterName), spec.Members, m.Spec.GetClusterDomain(), nil)...)
	}
	builder := connectionstring.Builder().
		SetName(m.Name).
		SetNamespace(m.Namespace).
		SetUsername(username).
		SetPassword(password).
		SetReplicas(m.Spec.Replicas()).
		SetService(m.Name + "-svc").
		SetPort(m.Spec.GetAdditionalMongodConfig().GetPortOrDefault()).
		SetVersion(m.Spec.GetMongoDBVersion()).
		SetAuthenticationModes(m.Spec.GetSecurityAuthenticationModes()).
		SetClusterDomain(m.Spec.GetClusterDomain()).
		SetExternalDomain(m.Spec.GetExternalDomain()).
		SetIsReplicaSet(true).
		SetIsTLSEnabled(m.Spec.IsSecurityTLSConfigEnabled()).
		SetHostnames(hostnames).
		SetScheme(scheme)

	return builder.Build()
}

func (m *MongoDBMultiCluster) GetAuthenticationModes() []string {
	return m.Spec.Security.Authentication.GetModes()
}

// getNextIndex returns the next higher index from the current cluster indexes
func getNextIndex(m map[string]int) int {
	maxi := -1

	for _, val := range m {
		maxi = intp.Max(maxi, val)
	}
	return maxi + 1
}

func (m *MongoDBMultiCluster) IsInChangeVersion() bool {
	spec, err := m.ReadLastAchievedSpec()
	if err != nil {
		return false
	}
	if spec != nil && (spec.Version != m.Spec.Version) {
		return true
	}
	return false
}

func (m *MongoDBMultiCluster) CalculateFeatureCompatibilityVersion() string {
	return fcv.CalculateFeatureCompatibilityVersion(m.Spec.Version, m.Status.FeatureCompatibilityVersion, m.Spec.FeatureCompatibilityVersion)
}
