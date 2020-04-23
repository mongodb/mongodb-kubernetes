package operator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io/ioutil"
	"math/big"
	"testing"
	"time"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"
	"github.com/10gen/ops-manager-kubernetes/pkg/kube/configmap"

	"github.com/stretchr/testify/assert"
	"go.uber.org/zap"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1"
	"github.com/10gen/ops-manager-kubernetes/pkg/controller/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	certsv1 "k8s.io/api/certificates/v1beta1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func configureX509(client *mock.MockedClient, condition certsv1.RequestConditionType) {
	cMap := x509ConfigMap()
	client.GetMapForObject(&corev1.ConfigMap{})[objectKeyFromApiObject(&cMap)] = &cMap
	createAgentCSRs(client, condition)
}

func TestX509CannotBeEnabled_IfAgentCertsAreNotApproved(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)

	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)

	// agent certs have not been approved yet
	configureX509(manager.Client, certsv1.CertificateDenied)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	expectedError := fmt.Sprintf("Agent certs have not yet been approved")
	checkReconcilePending(t, reconciler, rs, expectedError, manager.Client, 10)
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)
	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)
	configureX509(manager.Client, certsv1.CertificateApproved)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().EnableTLS().EnableX509().Build()
	manager := mock.NewManager(rs)
	addKubernetesTlsResources(manager.Client, rs)
	approveCSRs(manager.Client, rs)
	configureX509(manager.Client, certsv1.CertificateApproved)

	// enable internal cluster authentication mode
	rs.Spec.Security.ClusterAuthMode = util.X509

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	checkReconcileSuccessful(t, reconciler, rs, manager.Client)
}

func TestX509ClusterAuthentication_CanBeEnabled_IfX509AuthenticationIsEnabled_ShardedCluster(t *testing.T) {
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").Build()

	reconciler, client := defaultClusterReconciler(scWithTls)
	addKubernetesTlsResources(client, scWithTls)

	configureX509(client, certsv1.CertificateApproved)

	// enable internal cluster authentication mode
	scWithTls.Spec.Security.ClusterAuthMode = util.X509
	checkReconcileSuccessful(t, reconciler, scWithTls, client)
}

func TestX509CanBeEnabled_WhenThereAreOnlyTlsDeployments_ShardedCluster(t *testing.T) {
	scWithTls := DefaultClusterBuilder().EnableTLS().EnableX509().SetName("sc-with-tls").Build()

	reconciler, client := defaultClusterReconciler(scWithTls)
	addKubernetesTlsResources(client, scWithTls)

	configureX509(client, certsv1.CertificateApproved)

	checkReconcileSuccessful(t, reconciler, scWithTls, client)
}

func TestUpdateOmAuthentication_NoAuthenticationEnabled(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	processNames := []string{"my-rs-0", "my-rs-1", "my-rs-2"}

	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	r.updateOmAuthentication(conn, processNames, rs, zap.S())

	ac, _ := conn.ReadAutomationConfig()

	assert.True(t, ac.Auth.Disabled, "authentication was not specified to enabled, so it should remain disabled in Ops Manager")
	assert.Len(t, ac.Auth.Users, 0)
}

func TestUpdateOmAuthentication_EnableX509_TlsNotEnabled(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).Build()
	// deployment with existing non-tls non-x509 replica set
	conn := om.NewMockedOmConnection(createDeploymentFromReplicaSet(rs))

	// configure X509 authentication & tls
	rs.Spec.Security.Authentication.Modes = []string{"X509"}
	rs.Spec.Security.Authentication.Enabled = true
	rs.Spec.Security.TLSConfig.Enabled = true

	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring both options at once should not result in a failed status")
	assert.True(t, isMultiStageReconciliation, "configuring both tls and x509 at once should result in a multi stage reconciliation")
}

func TestUpdateOmAuthentication_EnableX509_WithTlsAlreadyEnabled(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().Build()
	conn := om.NewMockedOmConnection(createDeploymentFromReplicaSet(rs))
	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring x509 when tls has already been enabled should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if tls is already enabled, we should be able to configure x509 is a single reconciliation")
}

func TestUpdateOmAuthentication_EnableX509_FromEmptyDeployment(t *testing.T) {
	conn := om.NewMockedOmConnection(om.NewDeployment())

	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().EnableAuth().SetAuthModes([]string{"X509"}).Build()
	r := newReplicaSetReconciler(mock.NewManager(rs), om.NewEmptyMockedOmConnection)
	configureX509(r.client.(*mock.MockedClient), certsv1.CertificateApproved)
	status, isMultiStageReconciliation := r.updateOmAuthentication(conn, []string{"my-rs-0", "my-rs-1", "my-rs-2"}, rs, zap.S())

	assert.True(t, status.IsOK(), "configuring x509 and tls when there are no processes should not result in a failed status")
	assert.False(t, isMultiStageReconciliation, "if we are enabling tls and x509 at once, this should be done in a single reconciliation")
}

func TestX509AgentUserIsCorrectlyConfigured(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableTLS().EnableAuth().SetAuthModes([]string{"X509"}).Build()
	x509User := DefaultMongoDBUserBuilder().SetDatabase(util.X509Db).SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()

	// configure x509/tls resources
	addKubernetesTlsResources(manager.Client, rs)
	createAgentCSRs(manager.Client, certsv1.CertificateApproved)
	approveCSRs(manager.Client, rs)

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	})

	actual, err := userReconciler.Reconcile(requestFromObject(x509User))
	expected, _ := success()

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, "CN=mms-automation-agent,OU=MongoDB Kubernetes Operator,O=mms-automation-agent,L=NY,ST=NY,C=US")
}

func TestScramAgentUserIsCorrectlyConfigured(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").SetMembers(3).EnableAuth().SetAuthModes([]string{"SCRAM"}).Build()
	x509User := DefaultMongoDBUserBuilder().SetMongoDBResourceName("my-rs").Build()

	manager := mock.NewManager(rs)
	manager.Client.AddDefaultMdbConfigResources()

	reconciler := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)

	checkReconcileSuccessful(t, reconciler, rs, manager.Client)

	userReconciler := newMongoDBUserReconciler(manager, func(context *om.OMContext) om.Connection {
		return om.CurrMockedConnection // use the same connection
	})

	actual, err := userReconciler.Reconcile(requestFromObject(x509User))
	expected, _ := success()

	assert.NoError(t, err)
	assert.Equal(t, expected, actual)

	ac, _ := om.CurrMockedConnection.ReadAutomationConfig()
	assert.Equal(t, ac.Auth.AutoUser, util.AutomationAgentName)
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ReplicaSet(t *testing.T) {
	rs := DefaultReplicaSetBuilder().SetName("my-rs").
		SetMembers(3).
		EnableAuth().
		SetAuthModes([]string{"SCRAM"}).
		EnableX509InternalClusterAuth().
		Build()

	manager := mock.NewManager(rs)
	r := newReplicaSetReconciler(manager, om.NewEmptyMockedOmConnection)
	configureX509(r.client.(*mock.MockedClient), certsv1.CertificateApproved)
	addKubernetesTlsResources(r.client.(*mock.MockedClient), rs)

	checkReconcileSuccessful(t, r, rs, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
}

func TestX509InternalClusterAuthentication_CanBeEnabledWithScram_ShardedCluster(t *testing.T) {
	sc := DefaultClusterBuilder().SetName("my-sc").
		EnableAuth().
		SetAuthModes([]string{"SCRAM"}).
		EnableX509InternalClusterAuth().
		Build()

	manager := mock.NewManager(sc)
	r := newShardedClusterReconciler(manager, om.NewEmptyMockedOmConnection)
	configureX509(r.client.(*mock.MockedClient), certsv1.CertificateApproved)
	addKubernetesTlsResources(r.client.(*mock.MockedClient), sc)

	checkReconcileSuccessful(t, r, sc, manager.Client)

	currConn := om.CurrMockedConnection
	dep, _ := currConn.ReadDeployment()
	for _, p := range dep.ProcessesCopy() {
		assert.Equal(t, p.ClusterAuthMode(), "x509")
	}
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
	manager := mock.NewManager(rsWithTls)
	client := manager.Client
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
	manager := mock.NewManager(scWithTls)
	client := manager.Client
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

// createCSR creates a CSR object which can be set to either CertificateApproved or CertificateDenied
func createCSR(name, ns string, conditionType certsv1.RequestConditionType) certsv1.CertificateSigningRequest {
	return certsv1.CertificateSigningRequest{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s.%s", name, ns)},
		Spec: certsv1.CertificateSigningRequestSpec{
			Request: createMockCSRBytes(),
		},
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
// 		// 	createShardedClusterTLSData(client, mdb)
// 	}
// }

// addKubernetesTlsResources ensures all the required TLS secrets exist for the given MongoDB resource
func addKubernetesTlsResources(client *mock.MockedClient, mdb *mdbv1.MongoDB) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mock.TestCredentialsSecretName, Namespace: mock.TestNamespace},
		Data: map[string][]byte{
			"publicApiKey": []byte("someapi"),
			"user":         []byte("someuser"),
		},
	}
	_ = client.Update(context.TODO(), secret)
	switch mdb.Spec.ResourceType {
	case mdbv1.ReplicaSet:
		createReplicaSetTLSData(client, mdb)
	case mdbv1.ShardedCluster:
		createShardedClusterTLSData(client, mdb)
	}
}

// createMockCSRBytes creates a new Certificate Signing Request, signed with a
// fresh private key
func createMockCSRBytes() []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	template := x509.CertificateRequest{
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		DNSNames: []string{"somehost.com"},
	}
	certRequestBytes, err := x509.CreateCertificateRequest(rand.Reader, &template, priv)
	if err != nil {
		panic(err)
	}

	certRequestPemBytes := &bytes.Buffer{}
	pemBlock := pem.Block{Type: "CERTIFICATE REQUEST", Bytes: certRequestBytes}
	if err := pem.Encode(certRequestPemBytes, &pemBlock); err != nil {
		panic(err)
	}

	return certRequestPemBytes.Bytes()
}

// createMockCertAndKeyBytes generates a random key and certificate and returns
// them as bytes
func createMockCertAndKeyBytes() []byte {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		panic(err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	if err != nil {
		panic(err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"MongoDB"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), // cert expires in 10 years
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"somehost.com"},
	}
	certBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		panic(err)
	}

	certPemBytes := &bytes.Buffer{}
	if err := pem.Encode(certPemBytes, &pem.Block{Type: "CERTIFICATE", Bytes: certBytes}); err != nil {
		panic(err)
	}

	privPemBytes := &bytes.Buffer{}
	if err := pem.Encode(privPemBytes, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes}); err != nil {
		panic(err)
	}

	return append(certPemBytes.Bytes(), privPemBytes.Bytes()...)
}

// createReplicaSetTLSData creates and populates secrets required for a TLS enabled ReplicaSet
func createReplicaSetTLSData(client *mock.MockedClient, mdb *mdbv1.MongoDB) {
	// Lets create a secret with Certificates and private keys!
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-cert", mdb.Name),
			Namespace: mock.TestNamespace,
		},
	}

	certs := map[string][]byte{}
	clientCerts := map[string][]byte{}
	for i := 0; i < mdb.Spec.Members; i++ {
		pemFile := createMockCertAndKeyBytes()
		certs[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
		clientCerts[fmt.Sprintf("%s-%d-pem", mdb.Name, i)] = pemFile
	}
	secret.Data = certs
	_ = client.Create(context.TODO(), secret)

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       clientCerts,
	})
}

// createShardedClusterTLSData creates and populates all the  secrets needed for a TLS enabled Sharded
// Cluster with internal cluster authentication. Mongos, config server and all shards.
func createShardedClusterTLSData(client *mock.MockedClient, mdb *mdbv1.MongoDB) {
	// create the secrets for all the shards
	for i := 0; i < mdb.Spec.ShardCount; i++ {
		secretName := fmt.Sprintf("%s-%d-cert", mdb.Name, i)
		shardData := make(map[string][]byte)
		for j := 0; j <= mdb.Spec.MongodsPerShardCount; j++ {
			shardData[fmt.Sprintf("%s-%d-%d-pem", mdb.Name, i, j)] = createMockCertAndKeyBytes()
		}
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: mock.TestNamespace},
			Data:       shardData,
		})
		_ = client.Create(context.TODO(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-%d-clusterfile", mdb.Name, i), Namespace: mock.TestNamespace},
			Data:       shardData,
		})
	}

	// populate with the expected cert and key fields
	mongosData := make(map[string][]byte)
	for i := 0; i < mdb.Spec.MongosCount; i++ {
		mongosData[fmt.Sprintf("%s-mongos-%d-pem", mdb.Name, i)] = createMockCertAndKeyBytes()
	}

	// create the mongos secret
	mongosSecretName := fmt.Sprintf("%s-mongos-cert", mdb.Name)
	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: mongosSecretName, Namespace: mock.TestNamespace},
		Data:       mongosData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-mongos-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       mongosData,
	})

	// create secret for config server
	configData := make(map[string][]byte)
	for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
		configData[fmt.Sprintf("%s-config-%d-pem", mdb.Name, i)] = createMockCertAndKeyBytes()
	}

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-cert", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
	})

	_ = client.Create(context.TODO(), &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-config-clusterfile", mdb.Name), Namespace: mock.TestNamespace},
		Data:       configData,
	})

}

// approveAgentCSRs approves all the agent certs needed for x509 authentication
func approveAgentCSRs(client *mock.MockedClient) {
	// create the secret the agent certs will exist in
	createAgentCSRs(client, certsv1.CertificateApproved)
}

// createAgentCSRs creates all the agent CSRs needed for x509 at the specified condition type
func createAgentCSRs(client *mock.MockedClient, conditionType certsv1.RequestConditionType) {
	// create the secret the agent certs will exist in

	cert, _ := ioutil.ReadFile("testdata/certificates/certificate_then_key")
	client.GetMapForObject(&corev1.Secret{})[objectKey(mock.TestNamespace, util.AgentSecretName)] = &corev1.Secret{
		Data: map[string][]byte{
			util.AutomationAgentPemSecretKey: cert,
		},
	}

	addCsrs(client,
		createCSR("mms-automation-agent", mock.TestNamespace, conditionType),
		createCSR("mms-monitoring-agent", mock.TestNamespace, conditionType),
		createCSR("mms-backup-agent", mock.TestNamespace, conditionType),
	)
}

// approveCSRs approves all CSRs related to the given MongoDB resource, this includes TLS and x509 internal cluster authentication CSRs
func approveCSRs(client *mock.MockedClient, mdb *mdbv1.MongoDB) {
	if mdb != nil && mdb.Spec.Security.TLSConfig.Enabled {
		switch mdb.Spec.ResourceType {
		case mdbv1.ReplicaSet:
			for i := 0; i < mdb.Spec.Members; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-%d.%s", mdb.Name, i, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, i, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}
		case mdbv1.ShardedCluster:
			// mongos
			for i := 0; i < mdb.Spec.MongosCount; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-mongos-0.%s", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-mongos-0.%s-clusterfile", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}

			// config server
			for i := 0; i < mdb.Spec.ConfigServerCount; i++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-config-0.%s", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-config-0.%s-clusterfile", mdb.Name, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
			}

			// shards
			for shardNum := 0; shardNum < mdb.Spec.ShardCount; shardNum++ {
				addCsrs(client,
					createCSR(fmt.Sprintf("%s-%d.%s", mdb.Name, shardNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					createCSR(fmt.Sprintf("%s-%d.%s-clusterfile", mdb.Name, shardNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
				)
				for mongodNum := 0; mongodNum < mdb.Spec.MongodsPerShardCount; mongodNum++ {
					addCsrs(client,
						createCSR(fmt.Sprintf("%s-%d-%d.%s", mdb.Name, shardNum, mongodNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
						createCSR(fmt.Sprintf("%s-%d-%d.%s-clusterfile", mdb.Name, shardNum, mongodNum, mock.TestNamespace), mock.TestNamespace, certsv1.CertificateApproved),
					)
				}
			}
		}
	}
}

// x509ConfigMap returns a ConfigMap with x509 enabled
func x509ConfigMap() corev1.ConfigMap {
	return configmap.Builder().
		SetName(om.TestGroupName).
		SetNamespace(mock.TestNamespace).
		SetField(util.OmBaseUrl, om.TestURL).
		SetField(util.OmProjectName, om.TestGroupName).
		SetField(util.OmAuthMode, util.X509).
		Build()
}
