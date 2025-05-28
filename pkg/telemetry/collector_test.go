package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/api/v1/mdbmulti"
	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	searchv1 "github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	mcov1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	mockClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

const (
	testOperatorUUID           = "d05f3e84-8eb3-4eb4-a42a-9698a3350d46"
	testDatabaseStaticImage    = "static-image-mongodb-enterprise-server"
	testDatabaseNonStaticImage = "non-static-image"
)

func TestCollectDeploymentsSnapshot(t *testing.T) {
	tests := map[string]struct {
		objects                      []client.Object
		expectedEventsWithProperties []map[string]any
	}{
		"empty object list": {
			objects:                      []client.Object{},
			expectedEventsWithProperties: nil,
		},
		"single basic replicaset": {
			objects: []client.Object{
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Security: &mdbv1.Security{
								Authentication: &mdbv1.Authentication{
									Enabled: true,
									Modes:   []mdbv1.AuthMode{util.X509, util.LDAP, util.OIDC, util.SCRAM},
									Agents: mdbv1.AgentAuthentication{
										Mode: util.SCRAM,
									},
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID: "60c005b9-1a87-49de-b7d6-5ef9382d808f",
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"deploymentUID":            "60c005b9-1a87-49de-b7d6-5ef9382d808f",
					"operatorID":               testOperatorUUID,
					"architecture":             string(architectures.NonStatic),
					"isMultiCluster":           false,
					"type":                     "ReplicaSet",
					"IsRunningEnterpriseImage": false,
					"externalDomains":          ExternalDomainNone,
					"authenticationModeLDAP":   true,
					"authenticationModeOIDC":   true,
					"authenticationModeSCRAM":  true,
					"authenticationModeX509":   true,
					"authenticationAgentMode":  util.SCRAM,
				},
			},
		},
		"single basic multicluster replicaset": {
			objects: []client.Object{
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Security: &mdbv1.Security{
								Authentication: &mdbv1.Authentication{
									Enabled: true,
									Modes:   []mdbv1.AuthMode{util.SCRAM},
									Agents: mdbv1.AgentAuthentication{
										Mode: util.SCRAM,
									},
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID: "d5a53056-157a-4cda-96e9-fe48a9732990",
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"databaseClusters":         float64(0),
					"deploymentUID":            "d5a53056-157a-4cda-96e9-fe48a9732990",
					"operatorID":               testOperatorUUID,
					"architecture":             string(architectures.NonStatic),
					"isMultiCluster":           true,
					"type":                     "ReplicaSet",
					"IsRunningEnterpriseImage": false,
					"externalDomains":          ExternalDomainNone,
					"authenticationModeSCRAM":  true,
					"authenticationAgentMode":  util.SCRAM,
				},
			},
		},
		"single basic opsmanager": {
			objects: []client.Object{
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB:    omv1.AppDBSpec{},
						Topology: "Single",
					},
					ObjectMeta: metav1.ObjectMeta{
						UID: "7c338e30-8681-443d-aef4-ae4b17eb3a97",
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"deploymentUID":            "7c338e30-8681-443d-aef4-ae4b17eb3a97",
					"operatorID":               testOperatorUUID,
					"architecture":             string(architectures.NonStatic),
					"isMultiCluster":           false,
					"type":                     "OpsManager",
					"IsRunningEnterpriseImage": true,
					"externalDomains":          ExternalDomainNone,
				},
			},
		},
		"architecture annotation test": {
			objects: []client.Object{
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "c20a7cf1-a12d-4cee-a87e-7f61aa2bd878",
						Name: "test-rs-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.Static),
						},
					},
				},
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "97822e48-fb51-4ba5-9993-26841b44a7a3",
						Name: "test-rs-non-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.NonStatic),
						},
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "71368077-ea95-4564-acd6-09ec573fdf61",
						Name: "test-mrs-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.Static),
						},
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "a8a28c8a-6226-44fc-a8cd-e66a6942ffbd",
						Name: "test-mrs-non-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.NonStatic),
						},
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB:    omv1.AppDBSpec{},
						Topology: "Single",
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "0d76d2c9-98cd-4a80-a565-ba038d223ed0",
						Name: "test-om-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.Static),
						},
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB:    omv1.AppDBSpec{},
						Topology: "Single",
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "399680c7-e929-44f6-8b82-9be96a5e5533",
						Name: "test-om-non-static",
						Annotations: map[string]string{
							architectures.ArchitectureAnnotation: string(architectures.NonStatic),
						},
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"deploymentUID":            "c20a7cf1-a12d-4cee-a87e-7f61aa2bd878",
					"architecture":             string(architectures.Static),
					"IsRunningEnterpriseImage": true,
					"type":                     string(mdbv1.ReplicaSet),
					"isMultiCluster":           false,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
				},
				{
					"deploymentUID":            "97822e48-fb51-4ba5-9993-26841b44a7a3",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"type":                     string(mdbv1.ReplicaSet),
					"isMultiCluster":           false,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
				},
				{
					"deploymentUID":            "71368077-ea95-4564-acd6-09ec573fdf61",
					"architecture":             string(architectures.Static),
					"IsRunningEnterpriseImage": true,
					"type":                     string(mdbv1.ReplicaSet),
					"isMultiCluster":           true,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
					"databaseClusters":         float64(0),
				},
				{
					"deploymentUID":            "a8a28c8a-6226-44fc-a8cd-e66a6942ffbd",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"type":                     string(mdbv1.ReplicaSet),
					"isMultiCluster":           true,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
					"databaseClusters":         float64(0),
				},
				{
					"deploymentUID":            "0d76d2c9-98cd-4a80-a565-ba038d223ed0",
					"architecture":             string(architectures.Static),
					"IsRunningEnterpriseImage": true,
					"type":                     "OpsManager",
					"isMultiCluster":           false,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
				},
				{
					"deploymentUID":            "399680c7-e929-44f6-8b82-9be96a5e5533",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"type":                     "OpsManager",
					"isMultiCluster":           false,
					"externalDomains":          ExternalDomainNone,
					"operatorID":               testOperatorUUID,
				},
			},
		},
		"multicluster test": {
			objects: []client.Object{
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Topology:     mdbv1.ClusterTopologyMultiCluster,
						},
						ShardedClusterSpec: mdbv1.ShardedClusterSpec{
							ConfigSrvSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
									},
									{
										ClusterName: "cluster2",
										Members:     3,
									},
								},
							},
							MongosSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     2,
									},
									{
										ClusterName: "cluster2",
										Members:     3,
									},
								},
							},
							ShardSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
									},
									{
										ClusterName: "cluster2",
										Members:     1,
									},
									{
										ClusterName: "cluster3",
										Members:     1,
									},
									{
										ClusterName: "cluster4",
										Members:     1,
									},
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "1a58636d-6c10-49c9-a9ee-7c0fe80ac80c",
						Name: "test-msc",
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
						ClusterSpecList: []mdbv1.ClusterSpecItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     3,
							},
							{
								ClusterName: "cluster3",
								Members:     3,
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "a31ab7a8-e5bd-480b-afcc-ac2eec9ce348",
						Name: "test-mrs",
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB: omv1.AppDBSpec{
							ClusterSpecList: []mdbv1.ClusterSpecItem{
								{
									ClusterName: "cluster1",
									Members:     3,
								},
								{
									ClusterName: "cluster2",
									Members:     2,
								},
								{
									ClusterName: "cluster3",
									Members:     2,
								},
							},
							Topology: omv1.ClusterTopologyMultiCluster,
						},
						Topology: omv1.ClusterTopologyMultiCluster,
						ClusterSpecList: []omv1.ClusterSpecOMItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     1,
							},
						},
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "2b138678-4e4c-4be4-9877-16e6eaae279b",
						Name: "test-om-multi",
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"deploymentUID":            "1a58636d-6c10-49c9-a9ee-7c0fe80ac80c",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"databaseClusters":         float64(4),
					"isMultiCluster":           true,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
					"externalDomains":          ExternalDomainNone,
				},
				{
					"deploymentUID":            "a31ab7a8-e5bd-480b-afcc-ac2eec9ce348",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"databaseClusters":         float64(3),
					"isMultiCluster":           true,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
					"externalDomains":          ExternalDomainNone,
				},
				{
					"deploymentUID":            "2b138678-4e4c-4be4-9877-16e6eaae279b",
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"OmClusters":               float64(2),
					"appDBClusters":            float64(3),
					"isMultiCluster":           true,
					"operatorID":               testOperatorUUID,
					"type":                     "OpsManager",
					"externalDomains":          ExternalDomainNone,
				},
			},
		},
		"external domains test": {
			objects: []client.Object{
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Topology:     mdbv1.ClusterTopologySingleCluster,
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "2c60ec7b-b233-4d98-97e6-b7c423c19e24",
						Name: "test-msc-external-domains-none",
					},
				},
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Topology:     mdbv1.ClusterTopologySingleCluster,
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.default.domain"),
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "c7ccb57f-abd1-4944-8a99-02e5a79acf75",
						Name: "test-msc-external-domains-uniform",
					},
				},
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Topology:     mdbv1.ClusterTopologyMultiCluster,
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.default.domain"),
							},
						},
						ShardedClusterSpec: mdbv1.ShardedClusterSpec{
							ConfigSrvSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
										ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
											ExternalDomain: ptr.To("cluster1.domain"),
										},
									},
									{
										ClusterName: "cluster2",
										Members:     3,
										ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
											ExternalDomain: ptr.To("cluster2.domain"),
										},
									},
								},
							},
							MongosSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     2,
									},
									{
										ClusterName: "cluster2",
										Members:     3,
									},
								},
							},
							ShardSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
									},
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "7eed85ce-7a38-43ea-a338-6d959339c146",
						Name: "test-msc-external-domains-mixed",
					},
				},
				&mdbv1.MongoDB{
					Spec: mdbv1.MongoDbSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							Topology:     mdbv1.ClusterTopologyMultiCluster,
						},
						ShardedClusterSpec: mdbv1.ShardedClusterSpec{
							ConfigSrvSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
										ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
											ExternalDomain: ptr.To("cluster1.domain"),
										},
									},
									{
										ClusterName: "cluster2",
										Members:     3,
										ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
											ExternalDomain: ptr.To("cluster2.domain"),
										},
									},
								},
							},
							MongosSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     2,
									},
									{
										ClusterName: "cluster2",
										Members:     3,
									},
								},
							},
							ShardSpec: &mdbv1.ShardedClusterComponentSpec{
								ClusterSpecList: []mdbv1.ClusterSpecItem{
									{
										ClusterName: "cluster1",
										Members:     1,
									},
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "584515da-e797-48af-af7f-6561812c15f4",
						Name: "test-msc-external-domains-cluster-specific",
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
						ClusterSpecList: []mdbv1.ClusterSpecItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     3,
							},
							{
								ClusterName: "cluster3",
								Members:     3,
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "27b3d7cf-1f8b-434d-a002-ce85f7313507",
						Name: "test-mrs-external-domains-none",
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.default.domain"),
							},
						},
						ClusterSpecList: []mdbv1.ClusterSpecItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     3,
							},
							{
								ClusterName: "cluster3",
								Members:     3,
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "b050040e-7b53-4991-bae4-69663a523804",
						Name: "test-mrs-external-domains-uniform",
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.default.domain"),
							},
						},
						ClusterSpecList: []mdbv1.ClusterSpecItem{
							{
								ClusterName: "cluster1",
								Members:     1,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster1.domain"),
								},
							},
							{
								ClusterName: "cluster2",
								Members:     3,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster2.domain"),
								},
							},
							{
								ClusterName: "cluster3",
								Members:     3,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster3.domain"),
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "54427a32-1799-4a1b-b03f-a50484c09d2c",
						Name: "test-mrs-external-domains-mixed",
					},
				},
				&mdbmulti.MongoDBMultiCluster{
					Spec: mdbmulti.MongoDBMultiSpec{
						DbCommonSpec: mdbv1.DbCommonSpec{
							ResourceType: mdbv1.ReplicaSet,
						},
						ClusterSpecList: []mdbv1.ClusterSpecItem{
							{
								ClusterName: "cluster1",
								Members:     1,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster1.domain"),
								},
							},
							{
								ClusterName: "cluster2",
								Members:     3,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster2.domain"),
								},
							},
							{
								ClusterName: "cluster3",
								Members:     3,
								ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
									ExternalDomain: ptr.To("cluster3.domain"),
								},
							},
						},
					}, ObjectMeta: metav1.ObjectMeta{
						UID:  "fe6b6fad-51f2-4f98-8ddd-54ae24143ea6",
						Name: "test-mrs-external-domains-cluster-specific",
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB: omv1.AppDBSpec{},
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "5999bccb-d17d-4657-9ea6-ee9fa264d749",
						Name: "test-om-external-domains-none",
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB: omv1.AppDBSpec{
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.custom.domain"),
							},
						},
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "95808355-09d8-4a50-909a-e96c91c99665",
						Name: "test-om-external-domains-uniform",
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB: omv1.AppDBSpec{
							ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
								ExternalDomain: ptr.To("some.custom.domain"),
							},
							ClusterSpecList: []mdbv1.ClusterSpecItem{
								{
									ClusterName: "cluster1",
									Members:     3,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster1.domain"),
									},
								},
								{
									ClusterName: "cluster2",
									Members:     2,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster2.domain"),
									},
								},
								{
									ClusterName: "cluster3",
									Members:     2,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster3.domain"),
									},
								},
							},
							Topology: omv1.ClusterTopologyMultiCluster,
						},
						Topology: omv1.ClusterTopologyMultiCluster,
						ClusterSpecList: []omv1.ClusterSpecOMItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     1,
							},
						},
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "34daced5-b4ae-418b-bf38-034667e676ca",
						Name: "test-om-external-domains-mixed",
					},
				},
				&omv1.MongoDBOpsManager{
					Spec: omv1.MongoDBOpsManagerSpec{
						AppDB: omv1.AppDBSpec{
							ClusterSpecList: []mdbv1.ClusterSpecItem{
								{
									ClusterName: "cluster1",
									Members:     3,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster1.domain"),
									},
								},
								{
									ClusterName: "cluster2",
									Members:     2,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster2.domain"),
									},
								},
								{
									ClusterName: "cluster3",
									Members:     2,
									ExternalAccessConfiguration: &mdbv1.ExternalAccessConfiguration{
										ExternalDomain: ptr.To("cluster3.domain"),
									},
								},
							},
							Topology: omv1.ClusterTopologyMultiCluster,
						},
						Topology: omv1.ClusterTopologyMultiCluster,
						ClusterSpecList: []omv1.ClusterSpecOMItem{
							{
								ClusterName: "cluster1",
								Members:     1,
							},
							{
								ClusterName: "cluster2",
								Members:     1,
							},
						},
					},
					ObjectMeta: metav1.ObjectMeta{
						UID:  "cb365427-27d7-46dd-af31-cec0dad21bf0",
						Name: "test-om-external-domains-cluster-specific",
					},
				},
			},
			expectedEventsWithProperties: []map[string]any{
				{
					"deploymentUID":            "2c60ec7b-b233-4d98-97e6-b7c423c19e24",
					"externalDomains":          ExternalDomainNone,
					"isMultiCluster":           false,
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "c7ccb57f-abd1-4944-8a99-02e5a79acf75",
					"externalDomains":          ExternalDomainUniform,
					"isMultiCluster":           false,
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "7eed85ce-7a38-43ea-a338-6d959339c146",
					"externalDomains":          ExternalDomainMixed,
					"isMultiCluster":           true,
					"databaseClusters":         float64(2),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "584515da-e797-48af-af7f-6561812c15f4",
					"externalDomains":          ExternalDomainClusterSpecific,
					"isMultiCluster":           true,
					"databaseClusters":         float64(2),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "27b3d7cf-1f8b-434d-a002-ce85f7313507",
					"externalDomains":          ExternalDomainNone,
					"isMultiCluster":           true,
					"databaseClusters":         float64(3),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "b050040e-7b53-4991-bae4-69663a523804",
					"externalDomains":          ExternalDomainUniform,
					"isMultiCluster":           true,
					"databaseClusters":         float64(3),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "54427a32-1799-4a1b-b03f-a50484c09d2c",
					"externalDomains":          ExternalDomainMixed,
					"isMultiCluster":           true,
					"databaseClusters":         float64(3),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "fe6b6fad-51f2-4f98-8ddd-54ae24143ea6",
					"externalDomains":          ExternalDomainClusterSpecific,
					"isMultiCluster":           true,
					"databaseClusters":         float64(3),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": false,
					"operatorID":               testOperatorUUID,
					"type":                     string(mdbv1.ReplicaSet),
				},
				{
					"deploymentUID":            "5999bccb-d17d-4657-9ea6-ee9fa264d749",
					"externalDomains":          ExternalDomainNone,
					"isMultiCluster":           false,
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"operatorID":               testOperatorUUID,
					"type":                     "OpsManager",
				},
				{
					"deploymentUID":            "95808355-09d8-4a50-909a-e96c91c99665",
					"externalDomains":          ExternalDomainUniform,
					"isMultiCluster":           false,
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"operatorID":               testOperatorUUID,
					"type":                     "OpsManager",
				},
				{
					"deploymentUID":            "34daced5-b4ae-418b-bf38-034667e676ca",
					"externalDomains":          ExternalDomainMixed,
					"isMultiCluster":           true,
					"appDBClusters":            float64(3),
					"OmClusters":               float64(2),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"operatorID":               testOperatorUUID,
					"type":                     "OpsManager",
				},
				{
					"deploymentUID":            "cb365427-27d7-46dd-af31-cec0dad21bf0",
					"externalDomains":          ExternalDomainClusterSpecific,
					"isMultiCluster":           true,
					"appDBClusters":            float64(3),
					"OmClusters":               float64(2),
					"architecture":             string(architectures.NonStatic),
					"IsRunningEnterpriseImage": true,
					"operatorID":               testOperatorUUID,
					"type":                     "OpsManager",
				},
			},
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			k8sClient := mock.NewEmptyFakeClientBuilder().WithObjects(test.objects...).Build()
			mgr := mockClient.NewManagerWithClient(k8sClient)

			ctx := context.Background()

			beforeCallTimestamp := time.Now()
			events := collectDeploymentsSnapshot(ctx, mgr, testOperatorUUID, testDatabaseStaticImage, testDatabaseNonStaticImage)
			afterCallTimestamp := time.Now()

			require.Len(t, events, len(test.expectedEventsWithProperties), "expected and collected events count don't match")
			for _, expectedEventWithProperties := range test.expectedEventsWithProperties {
				deploymentUID := expectedEventWithProperties["deploymentUID"]
				event := findEventWithDeploymentUID(events, deploymentUID.(string))
				require.NotNil(t, "could not find event with deploymentUID %s", deploymentUID)

				assert.Equal(t, Deployments, event.Source)
				require.NotNilf(t, event.Timestamp, "event timestamp is nil for %s deployment", deploymentUID)
				assert.LessOrEqual(t, beforeCallTimestamp, event.Timestamp)
				assert.GreaterOrEqual(t, afterCallTimestamp, event.Timestamp)

				assert.Equal(t, expectedEventWithProperties, event.Properties)
			}
		})
	}
}

func findEventWithDeploymentUID(events []Event, deploymentUID string) *Event {
	for _, event := range events {
		if event.Properties["deploymentUID"] == deploymentUID {
			return &event
		}
	}

	return nil
}

type MockClient struct {
	client.Client
	MockList func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error
}

func (m *MockClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return m.MockList(ctx, list, opts...)
}

func TestAddCommunityEvents(t *testing.T) {
	operatorUUID := "test-operator-uuid"

	// Those 2 cases are when a customer uses Community reconciler to deploy enterprise or community MDB image
	testCases := []struct {
		name         string
		mongodbImage string
		isEnterprise bool
	}{
		{
			name:         "With community image",
			mongodbImage: "mongodb-community-server",
			isEnterprise: false,
		},
		{
			name:         "With enterprise image",
			mongodbImage: "mongodb-enterprise-server",
			isEnterprise: true,
		},
	}

	now := time.Now()

	for _, tc := range testCases {
		t.Run("With community resources", func(t *testing.T) {
			communityList := &mcov1.MongoDBCommunityList{
				Items: []mcov1.MongoDBCommunity{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("community-1"),
							Name: "test-community-1",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("community-2"),
							Name: "test-community-2",
						},
					},
				},
			}

			mc := &MockClient{
				MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if l, ok := list.(*mcov1.MongoDBCommunityList); ok {
						*l = *communityList
					}
					return nil
				},
			}

			events := addCommunityEvents(context.Background(), mc, operatorUUID, tc.mongodbImage, now)

			assert.Len(t, events, 2, "Should return 2 events for 2 community resources")

			assert.Equal(t, now, events[0].Timestamp)
			assert.Equal(t, Deployments, events[0].Source)
			assert.Equal(t, "community-1", events[0].Properties["deploymentUID"])
			assert.Equal(t, operatorUUID, events[0].Properties["operatorID"])
			assert.Equal(t, false, events[0].Properties["isMultiCluster"])
			assert.Equal(t, "Community", events[0].Properties["type"])
			assert.Equal(t, tc.isEnterprise, events[0].Properties["IsRunningEnterpriseImage"])

			assert.Equal(t, "community-2", events[1].Properties["deploymentUID"])
			assert.Equal(t, tc.isEnterprise, events[1].Properties["IsRunningEnterpriseImage"])
		})

		t.Run("With list error", func(t *testing.T) {
			mc := &MockClient{
				MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					return errors.New("list error")
				},
			}

			events := addCommunityEvents(context.Background(), mc, operatorUUID, tc.mongodbImage, now)

			assert.Empty(t, events, "Should return empty slice on list error")
		})

		t.Run("With empty list", func(t *testing.T) {
			mc := &MockClient{
				MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if l, ok := list.(*mcov1.MongoDBCommunityList); ok {
						*l = mcov1.MongoDBCommunityList{}
					}
					return nil
				},
			}

			events := addCommunityEvents(context.Background(), mc, operatorUUID, tc.mongodbImage, now)

			assert.Empty(t, events, "Should return empty slice for empty community list")
		})
	}
}

func TestAddSearchEvents(t *testing.T) {
	operatorUUID := "test-operator-uuid"

	now := time.Now()

	testCases := []struct {
		name      string
		resources searchv1.MongoDBSearchList
		events    []DeploymentUsageSnapshotProperties
	}{
		{
			name: "With resources",
			resources: searchv1.MongoDBSearchList{
				Items: []searchv1.MongoDBSearch{
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("search-1"),
							Name: "test-search-1",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							UID:  types.UID("search-2"),
							Name: "test-search-2",
						},
					},
				},
			},
			events: []DeploymentUsageSnapshotProperties{
				{
					DeploymentUID:            "search-1",
					OperatorID:               operatorUUID,
					Architecture:             string(architectures.Static),
					IsMultiCluster:           false,
					Type:                     "Search",
					IsRunningEnterpriseImage: false,
				},
				{
					DeploymentUID:            "search-2",
					OperatorID:               operatorUUID,
					Architecture:             string(architectures.Static),
					IsMultiCluster:           false,
					Type:                     "Search",
					IsRunningEnterpriseImage: false,
				},
			},
		},
		{
			name:      "With no resources",
			resources: searchv1.MongoDBSearchList{},
			events:    []DeploymentUsageSnapshotProperties{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mc := &MockClient{
				MockList: func(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
					if l, ok := list.(*searchv1.MongoDBSearchList); ok {
						*l = tc.resources
					}
					return nil
				},
			}

			events := addSearchEvents(context.Background(), mc, operatorUUID, now)
			expectedEvents := make([]Event, len(tc.events))
			for i, event := range tc.events {
				expectedEvents[i] = *createEvent(event, now, Deployments)
			}

			assert.ElementsMatch(t, expectedEvents, events, "Should return the expected events")
		})
	}
}
