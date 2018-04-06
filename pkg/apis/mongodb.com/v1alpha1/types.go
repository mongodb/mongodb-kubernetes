package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbReplicaSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbReplicaSetSpec `json:"spec"`
}

type MongoDbReplicaSetSpec struct {
	Members int32  `json:"members"`
	Version string `json:"mongodb_version"`
	// this is an optional service, it will get the name "<rsName>-service" in case not provided
	Service *string `json:"service, omitempty"`
	// this is the name of config map containing information about OpsManager connection parameters
	OmConfigName string `json:"ops_manager_config"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbReplicaSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbReplicaSet `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbStandalone struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbStandaloneSpec `json:"spec"`
}

type MongoDbStandaloneSpec struct {
	Version string `json:"mongodb_version"`
	// this is an optional service, it will get the name "<standaloneName>-service" in case not provided
	Service *string `json:"service, omitempty"`
	// this is the name of config map containing information about OpsManager connection parameters
	OmConfigName string `json:"ops_manager_config"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbStandaloneList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbStandalone `json:"items"`
}

// +genclient
// +genclient:noStatus
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata"`
	Spec              MongoDbShardedClusterSpec `json:"spec"`
}

type MongoDbShardedClusterSpec struct {
	HostName string `json:"hostname"`
	Shards   int32
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

type MongoDbShardedClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MongoDbShardedCluster `json:"items"`
}
