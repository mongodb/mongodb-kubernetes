package operator

import (
	"context"
	"fmt"
	"testing"

	v1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestX509CannotBeEnabled_IfAgentCertsAreNotApproved(t *testing.T) {
	cMap := x509ConfigMap()
	manager := newMockedManager(cMap)
	client := manager.client

	// agent certs have not been approved yet
	createAgentCSRs(client, certsv1.CertificateDenied)

	projectController := newProjectReconciler(manager, om.NewEmptyMockedOmConnection)
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))
	expected, _ := retry()
	assert.Nil(t, projectErr)
	assert.Equal(t, expected, projectResult, "should not succeed if there are still pending agent CSRs")
}

func TestX509InternalClusterAuthentication_CannotBeEnabledForReplicaSet_IfProjectLevelX509AuthenticationIsNotEnabled(t *testing.T) {
	rsWithTls := DefaultReplicaSetBuilder().EnableTLS().SetClusterAuth(util.X509).SetName("rs-with-tls").Build()

	manager := newMockedManager(rsWithTls)
	client := manager.client
	addKubernetesTlsResources(client, rsWithTls)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileFailed(t, reconciler, rsWithTls,
		"This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509", client)

}

func TestX509InternalClusterAuthentication_CannotBeEnabledForShardedCluster_IfProjectLevelX509AuthenticationIsNotEnabled(t *testing.T) {
	scWithTls := DefaultClusterBuilder().WithTLS().SetClusterAuth(util.X509).SetName("sc-with-tls").Build()
	manager := newMockedManager(scWithTls)
	client := manager.client
	addKubernetesTlsResources(client, scWithTls)
	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileFailed(t, reconciler, scWithTls,
		"This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509", client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ReplicaSet(t *testing.T) {

	rsWithTls := DefaultReplicaSetBuilder().EnableTLS().SetName("rs-with-tls").Build()

	manager := newMockedManager(rsWithTls)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	addKubernetesTlsResources(client, rsWithTls)

	// create the plain TLS replica set
	checkReconcileSuccessful(t, reconciler, rsWithTls, client)

	// enable internal cluster authentication mode
	rsWithTls.Spec.Security.ClusterAuthMode = util.X509

	checkReconcileFailed(t, reconciler, rsWithTls,
		"This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509", client)

	cMap := enableProjectLevelX509Authentication(client)
	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expected, _ := success()
	assert.Nil(t, projectErr)
	assert.Equal(t, expected, projectResult,
		"should be able to enable x509 internal cluster auth if x509 auth is disabled at the project level")

	checkReconcileSuccessful(t, reconciler, rsWithTls, client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ShardedCluster(t *testing.T) {

	scWithTls := DefaultClusterBuilder().WithTLS().SetName("sc-with-tls").Build()

	manager := newMockedManager(scWithTls)
	client := manager.client

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	addKubernetesTlsResources(client, scWithTls)

	// create the plain TLS sharded cluster
	checkReconcileSuccessful(t, reconciler, scWithTls, client)

	// enable internal cluster authentication mode
	scWithTls.Spec.Security.ClusterAuthMode = util.X509

	checkReconcileFailed(t, reconciler, scWithTls,
		"This deployment has clusterAuthenticationMode set to x509, ensure the ConfigMap for this project is configured to enable x509", client)

	cMap := enableProjectLevelX509Authentication(client)
	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expected, _ := success()
	assert.Nil(t, projectErr)
	assert.Equal(t, expected, projectResult, "should be able to enable x509 internal cluster auth if x509 auth is disabled at the project level")

	checkReconcileSuccessful(t, reconciler, scWithTls, client)
}

func TestX509CannotBeEnabled_WhenThereIsANonTlsDeployment_ReplicaSet(t *testing.T) {
	rsWithoutTls := DefaultReplicaSetBuilder().SetName("rs-without-tls").Build()

	manager := newMockedManager(rsWithoutTls)
	client := manager.client

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rsWithoutTls, client)

	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})

	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := retry()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult,
		"the request should have been requeued because it should not be possible to enable x509 at the project level when there are any non tls deployments")

}

func TestX509CannotBeEnabled_WhenThereIsANonTlsDeployment_ShardedCluster(t *testing.T) {
	scWithoutTls := DefaultClusterBuilder().SetName("sc-without-tls").Build()

	manager := newMockedManager(scWithoutTls)
	client := manager.client

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, scWithoutTls, client)

	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	})

	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := retry()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult,
		"the request should have been requeued because it should not be possible to enable x509 at the project level when there are any non tls deployments")

}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ReplicaSet(t *testing.T) {
	rsWithTls := DefaultReplicaSetBuilder().EnableTLS().SetName("rs-with-tls").Build()
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	manager := newMockedManager(rsWithTls)
	client := manager.client
	addKubernetesTlsResources(client, rsWithTls)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rsWithTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := success()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "x509 should be successfully enabled when all deployments are tls enabled")
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ShardedCluster(t *testing.T) {
	scWithTls := DefaultClusterBuilder().WithTLS().SetName("sc-with-tls").Build()
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	manager := newMockedManager(scWithTls)
	client := manager.client
	addKubernetesTlsResources(client, scWithTls)

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, scWithTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := success()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "x509 should be successfully enabled when all deployments are tls enabled")
}

/*

// TODO: Design a strategy for this particular case. These tests are going to be reworked as part of the
// SCRAM-SHA epic.
//
// https://jira.mongodb.org/browse/CLOUDP-49894
//
func TestX509CannotBeEnabled_WhenThereAreBothTlsAndNonTlsDeployments_ReplicaSet(t *testing.T) {

	rsWithoutTls := DefaultReplicaSetBuilder().SetName("rs-without-tls").Build()
	rsWithTls := DefaultReplicaSetBuilder().EnableTLS().SetName("rs-with-tls").Build()

	// we need to re-use the same connection between different controllers
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	// create a MongoDB resource with TLS enabled
	manager := newMockedManager(rsWithTls)
	client := manager.client
	addKubernetesTlsResources(client, rsWithTls)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rsWithTls, client)

	// create a MongoDB resource with TLS disabled
	_ = client.Create(context.TODO(), rsWithoutTls)
	checkReconcileSuccessful(t, reconciler, rsWithoutTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := retry()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "the request should have been requeued")

}

func TestX509CannotBeEnabled_WhenThereAreBothTlsAndNonTlsDeployments_ShardedCluster(t *testing.T) {

	scWithoutTls := DefaultClusterBuilder().SetName("sc-without-tls").Build()
	scWithTls := DefaultClusterBuilder().WithTLS().SetName("sc-with-tls").Build()

	// we need to re-use the same connection between different controllers
	connectionFunc := func(omContext *om.OMContext) om.Connection {
		return om.CurrMockedConnection
	}

	// create a MongoDB resource with TLS enabled
	manager := newMockedManager(scWithTls)
	client := manager.client
	addKubernetesTlsResources(client, scWithTls)

	reconciler := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, scWithTls, client)

	// create a MongoDB resource with TLS disabled
	_ = client.Create(context.TODO(), scWithoutTls)
	checkReconcileSuccessful(t, reconciler, scWithoutTls, client)

	// enable x509 authentication at the project level
	cMap := enableProjectLevelX509Authentication(client)

	// our project controller needs to use the same connection so it shares the underlying deployment
	projectController := newProjectReconciler(manager, connectionFunc)
	projectResult, projectErr := projectController.Reconcile(requestFromObject(cMap))

	expectedResult, _ := retry()
	assert.Nil(t, projectErr, "it should not be possible to enable x509 at the project level when not all deployments are tls enabled")
	assert.Equal(t, expectedResult, projectResult, "the request should have been requeued")

}

*/

// enableProjectLevelX509Authentication creates a ConfigMap which has x509 authentication enabled and credentials specified
// it will also create pre-approved CSRs for all the agents.
func enableProjectLevelX509Authentication(client *MockedClient) *corev1.ConfigMap {
	cMap := x509ConfigMap()
	_ = client.Update(context.TODO(), cMap)
	// populate client with pre-approved CSRs for the generated agent certs
	approveAgentCSRs(client)
	return cMap
}

// x509ConfigMap returns a ConfigMap with x509 enabled
func x509ConfigMap() *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: om.TestGroupName, Namespace: TestNamespace},
		Data: map[string]string{
			util.OmBaseUrl:     om.TestURL,
			util.OmAuthMode:    util.X509,
			util.OmProjectName: om.TestGroupName,
			util.OmCredentials: TestCredentialsSecretName,
		},
	}
}

// createCSR creates a CSR object which can be set to either CertificateApproved or CertificateDenied
func createCSR(conditionType certsv1.RequestConditionType) *certsv1.CertificateSigningRequest {
	return &certsv1.CertificateSigningRequest{
		Status: certsv1.CertificateSigningRequestStatus{
			Conditions: []certsv1.CertificateSigningRequestCondition{
				{Type: conditionType}}}}
}

// TODO: Add this function instead of having all the client/server Secret with certs
// generated in the same function
// func addClientx509Certificates(client *MockedClient, mdb *v1.MongoDB) {
// 	switch mdb.Spec.ResourceType {
// 	case v1.ReplicaSet:
// 		createReplicaSetTLSData(client, mdb)
// 		// TODO: Add Sharded Cluster
// 		// case v1.ShardedCluster:
// 		// 	createShardedClusterSecretData(client, mdb)
// 	}
// }

// addKubernetesTlsResources ensures all the required TLS secrets exist for the given MongoDB resource
func addKubernetesTlsResources(client *MockedClient, mdb *v1.MongoDB) {
	switch mdb.Spec.ResourceType {
	case v1.ReplicaSet:
		createReplicaSetTLSData(client, mdb)
	case v1.ShardedCluster:
		createShardedClusterSecretData(client, mdb)
	}
}

func createCertsAndKey() []byte {
	return []byte(`-----BEGIN CERTIFICATE-----
some certificate
-----END CERTIFICATE-----
-----BEGIN RSA PRIVATE KEY-----
some private key
-----END RSA PRIVATE KEY-----`)
}

// createReplicaSetTLSData creates and populates secrets required for a TLS enabled ReplicaSet
func createReplicaSetTLSData(client *MockedClient, mdb *v1.MongoDB) {
	// First lets create a Credentials Object
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: TestCredentialsSecretName, Namespace: TestNamespace},
	}
	data := map[string][]byte{
		"publicApiKey": []byte("someapi"),
		"user":         []byte("someuser"),
	}

	secret.Data = data
	_ = client.Update(context.TODO(), secret)

	// Second, lets create a secret with Certificates and private keys!
	secret = &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", mdb.Name),
			Namespace: TestNamespace,
		},
	}

	certs := map[string][]byte{}
	clientCerts := map[string][]byte{}
	for i := 0; i < mdb.Spec.Members; i++ {
		pemFile := createCertsAndKey()
		certs[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
		clientCerts[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
	}
	secret.Data = certs
	_ = client.Create(context.TODO(), secret)

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-clusterfile", mdb.Name), Namespace: TestNamespace},
		Data:       clientCerts,
	})
}

// createShardedClusterSecretData creates and populates all the  secrets needed for a TLS enabled Sharded
// Cluster with internal cluster authentication. Mongos, config server and all shards.
func createShardedClusterSecretData(client *MockedClient, mdb *v1.MongoDB) {
	// create the secrets for all the shards
	for i := 0; i < mdb.Spec.ShardCount; i++ {
		secretName := fmt.Sprintf("%s-%d-cert", mdb.Name, i)
		shardData := make(map[string][]byte, 0)
		for j := 0; j <= mdb.Spec.MongodsPerShardCount; j++ {
			shardData[fmt.Sprintf("%s-%d-%d-pem", mdb.Name, i, j)] = createCertsAndKey()
		}
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: TestNamespace},
			Data:       shardData,
		})
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d-clusterfile", mdb.Name, i), Namespace: TestNamespace},
			Data:       shardData,
		})
	}

	// populate with the expected cert and key fields
	mongosData := make(map[string][]byte, 0)
	for i := 0; i < mdb.Spec.MongosCount; i++ {
		mongosData[fmt.Sprintf("%s-mongos-%d-pem", mdb.Name, i)] = createCertsAndKey()
	}

	// create the mongos secret
	mongosSecretName := fmt.Sprintf("%s-mongos-cert", mdb.Name)
	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mongosSecretName, Namespace: TestNamespace},
		Data:       mongosData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-mongos-clusterfile", mdb.Name), Namespace: TestNamespace},
		Data:       mongosData,
	})

	// create secret for config server
	configData := make(map[string][]byte, 0)
	for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
		configData[fmt.Sprintf("%s-config-%d-pem", mdb.Name, i)] = createCertsAndKey()
	}

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-cert", mdb.Name), Namespace: TestNamespace},
		Data:       configData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-clusterfile", mdb.Name), Namespace: TestNamespace},
		Data:       configData,
	})

}

// approveAgentCSRs approves all the agent certs needed for x509 authentication
func approveAgentCSRs(client *MockedClient) {
	// create the secret the agent certs will exist in
	createAgentCSRs(client, certsv1.CertificateApproved)
}

// createAgentCSRs creates all the agent CSRs needed for x509 at the specified condition type
func createAgentCSRs(client *MockedClient, conditionType certsv1.RequestConditionType) {
	// create the secret the agent certs will exist in
	client.secrets[objectKey(TestNamespace, util.AgentSecretName)] = &corev1.Secret{}
	client.csrs[objectKey("", fmt.Sprintf("mms-automation-agent.%s", TestNamespace))] = createCSR(conditionType)
	client.csrs[objectKey("", fmt.Sprintf("mms-monitoring-agent.%s", TestNamespace))] = createCSR(conditionType)
	client.csrs[objectKey("", fmt.Sprintf("mms-backup-agent.%s", TestNamespace))] = createCSR(conditionType)
}

// approveCSRs approves all CSRs related to the given MongoDB resource, this includes TLS and x509 internal cluster authentication CSRs
func approveCSRs(client *MockedClient, mdb *v1.MongoDB) {
	if mdb != nil && mdb.Spec.Security.TLSConfig.Enabled {
		switch mdb.Spec.ResourceType {
		case v1.ReplicaSet:
			for i := 0; i < mdb.Spec.Members; i++ {
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d.%s", mdb.Name, i, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, i, TestNamespace))] = createCSR(certsv1.CertificateApproved)
			}
		case v1.ShardedCluster:
			// mongos
			for i := 0; i < mdb.Spec.MongosCount; i++ {
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-mongos-0.%s", mdb.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-mongos-0.%s-clusterfile", mdb.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
			}

			// config server
			for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-config-0.%s", mdb.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-config-0.%s-clusterfile", mdb.Name, TestNamespace))] = createCSR(certsv1.CertificateApproved)
			}

			// shards
			for shardNum := 0; shardNum < mdb.Spec.ShardCount; shardNum++ {
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d.%s", mdb.Name, shardNum, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, shardNum, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				for mongodNum := 0; mongodNum < mdb.Spec.MongodsPerShardCount; mongodNum++ {
					client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d-%d.%s", mdb.Name, shardNum, mongodNum, TestNamespace))] = createCSR(certsv1.CertificateApproved)
					client.csrs[objectKey(TestNamespace, fmt.Sprintf("%s-%d-%d.%s-clusterfile", mdb.Name, shardNum, mongodNum, TestNamespace))] = createCSR(certsv1.CertificateApproved)
				}
			}
		}
	}
}
