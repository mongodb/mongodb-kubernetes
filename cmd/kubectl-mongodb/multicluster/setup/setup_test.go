package setup

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/kubernetes/fake"

	cryptorand "crypto/rand"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/kubectl-mongodb/common"
)

const (
	testCentralCluster = "operator-cluster"
	testNamespace      = "mongodb"
	testServiceAccount = "mongodb-kubernetes-operator-multi-cluster"
)

// only the clusters are read out of the KubeConfig, to resolve the member cluster api server urls.
const testKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://api.operator-cluster
  name: operator-cluster
- cluster:
    server: https://api.member-cluster-0
  name: member-cluster-0
- cluster:
    server: https://api.member-cluster-1
  name: member-cluster-1
- cluster:
    server: https://api.member-cluster-2
  name: member-cluster-2
`

var testMemberClusters = []string{"member-cluster-0", "member-cluster-1", "member-cluster-2"}

func TestSetup_WithoutMemberClusterCA_KeepsServiceAccountCA(t *testing.T) {
	kubeConfig, _ := runSetup(t, nil)

	require.Len(t, kubeConfig.Clusters, len(testMemberClusters))
	for i, kubeConfigCluster := range kubeConfig.Clusters {
		assert.Equal(t, testMemberClusters[i], kubeConfigCluster.Name)
		assert.Equal(t, serviceAccountCA(testMemberClusters[i]), kubeConfigCluster.Cluster.CertificateAuthorityData, "Without --member-cluster-ca the CA should be read from the Service Account token Secret.")
	}
}

func TestSetup_WithMemberClusterCA_OverridesOnlyThatCluster(t *testing.T) {
	customCA := generateCAPEM(t, "external-terminator-for-member-cluster-0")
	customCAPath := filepath.Join(t.TempDir(), "member-cluster-0.pem")
	require.NoError(t, os.WriteFile(customCAPath, customCA, 0o600))

	kubeConfig, _ := runSetup(t, nil, "--member-cluster-ca", fmt.Sprintf("%s=%s", testMemberClusters[0], customCAPath))

	require.Len(t, kubeConfig.Clusters, len(testMemberClusters))
	assert.Equal(t, testMemberClusters[0], kubeConfig.Clusters[0].Name)
	assert.Equal(t, customCA, kubeConfig.Clusters[0].Cluster.CertificateAuthorityData, "The CA file named on the command line should replace the Service Account token Secret CA.")

	for i := 1; i < len(testMemberClusters); i++ {
		assert.Equal(t, serviceAccountCA(testMemberClusters[i]), kubeConfig.Clusters[i].Cluster.CertificateAuthorityData, "Clusters not named on the command line should keep the Service Account token Secret CA.")
	}
}

func TestSetup_WarnsWhenAnExistingCustomCAWouldBeReplaced(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup=%t", cleanup), func(t *testing.T) {
			seededCAs := map[string][]byte{testMemberClusters[0]: []byte("a custom CA left behind by an earlier run")}

			// the KubeConfig Secret carries the label --cleanup deletes by, so the cleanup case
			// also pins that the existing CA is read before that happens
			var extraArgs []string
			if cleanup {
				extraArgs = append(extraArgs, "--cleanup")
			}
			kubeConfig, output := runSetup(t, seededCAs, extraArgs...)

			assert.Contains(t, output, fmt.Sprintf("Warning: replacing the CA for member cluster %s", testMemberClusters[0]))
			assert.Contains(t, output, fmt.Sprintf("--member-cluster-ca %s=<path-to-pem-file>", testMemberClusters[0]))
			assert.Equal(t, 1, strings.Count(output, "Warning:"), "Only the cluster whose CA is being replaced should be warned about.")

			require.Len(t, kubeConfig.Clusters, len(testMemberClusters))
			assert.Equal(t, serviceAccountCA(testMemberClusters[0]), kubeConfig.Clusters[0].Cluster.CertificateAuthorityData, "The warning should describe a replacement that then actually happens.")
		})
	}
}

func TestSetup_DoesNotWarnWhenTheExistingKubeConfigHasNoCAForTheCluster(t *testing.T) {
	// a hand written KubeConfig can point at a CA file instead of inlining it, which unmarshals
	// to a cluster entry that is present but holds no bytes
	seededCAs := map[string][]byte{testMemberClusters[0]: nil}

	_, output := runSetup(t, seededCAs)

	assert.NotContains(t, output, "Warning:", "A cluster with no certificate-authority-data has no custom CA to lose.")
}

func TestSetup_DoesNotWarnOnAPlainRerun(t *testing.T) {
	// what the command itself wrote last time, so this also pins that the CA survives the
	// round trip through the KubeConfig Secret byte for byte
	seededCAs := map[string][]byte{}
	for _, clusterName := range testMemberClusters {
		seededCAs[clusterName] = serviceAccountCA(clusterName)
	}

	_, output := runSetup(t, seededCAs)

	assert.NotContains(t, output, "Warning:", "Re-running the command with nothing changed should stay quiet.")
}

func TestSetup_DoesNotWarnWhenTheCustomCAIsPassedAgain(t *testing.T) {
	customCA := generateCAPEM(t, "external-terminator-for-member-cluster-0")
	customCAPath := filepath.Join(t.TempDir(), "member-cluster-0.pem")
	require.NoError(t, os.WriteFile(customCAPath, customCA, 0o600))

	seededCAs := map[string][]byte{testMemberClusters[0]: customCA}
	_, output := runSetup(t, seededCAs, "--member-cluster-ca", fmt.Sprintf("%s=%s", testMemberClusters[0], customCAPath))

	assert.NotContains(t, output, "Warning:", "Re-passing --member-cluster-ca should not warn about the CA it keeps in place.")
}

func TestSetup_WarnsAndContinuesWhenTheExistingKubeConfigCannotBeRead(t *testing.T) {
	// a KubeConfig Secret the tool cannot parse must not stop it writing a good one: that is the
	// only way out for anyone who hand patched that Secret before --member-cluster-ca existed
	existingKubeConfigs := map[string][]byte{
		"NotAKubeConfig":                      []byte("]not a kubeconfig["),
		"CertificateAuthorityDataIsNotBase64": []byte("clusters:\n- name: member-cluster-0\n  cluster:\n    certificate-authority-data: -----BEGIN CERTIFICATE-----\n"),
	}

	for name, existingKubeConfig := range existingKubeConfigs {
		t.Run(name, func(t *testing.T) {
			kubeConfig, output := runSetupWithExistingKubeConfig(t, existingKubeConfig)

			assert.Contains(t, output, "Warning: not checking for a replaced custom CA")

			require.Len(t, kubeConfig.Clusters, len(testMemberClusters))
			assert.Equal(t, serviceAccountCA(testMemberClusters[0]), kubeConfig.Clusters[0].Cluster.CertificateAuthorityData, "The command should still write a usable KubeConfig.")
		})
	}
}

func TestParseSetupFlags_RejectsAMemberClusterCAForAnUnknownCluster(t *testing.T) {
	err := parseFlags(t, "--member-cluster-ca", "not-a-member-cluster=/dev/null")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not one of the member clusters")
}

// parseFlags points the command at a KubeConfig holding the test clusters and feeds it argv the way
// the CLI does. The cobra flag set and the parsed flags are package level state that outlives a
// single test, so they are reset on the way in.
func parseFlags(t *testing.T, extraArgs ...string) error {
	t.Helper()

	kubeConfigPath := filepath.Join(t.TempDir(), "kubeconfig")
	require.NoError(t, os.WriteFile(kubeConfigPath, []byte(testKubeconfig), 0o600))
	t.Setenv("KUBECONFIG", kubeConfigPath)

	common.MemberClusterCAFiles = nil
	setupFlags.MemberClusterApiServerUrls = nil
	setupFlags.Cleanup = false

	require.NoError(t, SetupCmd.Flags().Parse(append([]string{
		"--member-clusters", strings.Join(testMemberClusters, ","),
		"--central-cluster", testCentralCluster,
		"--member-cluster-namespace", testNamespace,
		"--central-cluster-namespace", testNamespace,
		"--service-account", testServiceAccount,
	}, extraArgs...)))

	return parseSetupFlags()
}

// runSetup drives the setup command the way the CLI does and returns the KubeConfig the Operator
// would read from its Secret, along with everything the command printed. seededCAs, when given, is
// the KubeConfig an earlier run of the command left behind.
func runSetup(t *testing.T, seededCAs map[string][]byte, extraArgs ...string) (common.KubeConfigFile, string) {
	t.Helper()

	var existingKubeConfig []byte
	if len(seededCAs) > 0 {
		clusters := make([]common.KubeConfigClusterItem, 0, len(seededCAs))
		for clusterName, ca := range seededCAs {
			clusters = append(clusters, common.KubeConfigClusterItem{Name: clusterName, Cluster: common.KubeConfigCluster{CertificateAuthorityData: ca}})
		}

		var err error
		existingKubeConfig, err = yaml.Marshal(common.KubeConfigFile{Clusters: clusters})
		require.NoError(t, err)
	}

	return runSetupWithExistingKubeConfig(t, existingKubeConfig, extraArgs...)
}

func runSetupWithExistingKubeConfig(t *testing.T, existingKubeConfig []byte, extraArgs ...string) (common.KubeConfigFile, string) {
	t.Helper()

	require.NoError(t, parseFlags(t, extraArgs...))

	ctx := context.Background()
	clientMap := fakeClientMap(t, setupFlags, existingKubeConfig)
	output := captureStdout(t, func() {
		require.NoError(t, common.EnsureMultiClusterResources(ctx, setupFlags, clientMap))
	})

	secret, err := clientMap[setupFlags.CentralCluster].CoreV1().Secrets(setupFlags.CentralClusterNamespace).Get(ctx, common.KubeConfigSecretName, metav1.GetOptions{})
	require.NoError(t, err)

	kubeConfig := common.KubeConfigFile{}
	require.NoError(t, yaml.Unmarshal(secret.Data[common.KubeConfigSecretKey], &kubeConfig))

	return kubeConfig, output
}

// captureStdout collects everything the command prints, which is where its warnings go.
func captureStdout(t *testing.T, run func()) string {
	t.Helper()

	reader, writer, err := os.Pipe()
	require.NoError(t, err)

	original := os.Stdout
	os.Stdout = writer
	// run() may call t.FailNow, so every later test would print into this pipe without the defer
	defer func() { os.Stdout = original }()

	// drained concurrently, the command prints more than a pipe buffer holds
	captured := make(chan string, 1)
	go func() {
		buf := bytes.Buffer{}
		_, _ = io.Copy(&buf, reader)
		captured <- buf.String()
	}()

	run()

	require.NoError(t, writer.Close())

	return <-captured
}

// fakeClientMap returns a client for every cluster, each one pre seeded with the Service Account
// token Secret that k8s would have populated. existingKubeConfig, when given, also leaves a
// KubeConfig Secret in the central cluster, as an earlier run of the command would have.
func fakeClientMap(t *testing.T, flags common.Flags, existingKubeConfig []byte) map[string]common.KubeClient {
	t.Helper()

	clientMap := map[string]common.KubeClient{}
	for _, clusterName := range append(slices.Clone(flags.MemberClusters), flags.CentralCluster) {
		tokenSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-token-secret", flags.ServiceAccount),
				Namespace: flags.CentralClusterNamespace,
			},
			Type: corev1.SecretTypeServiceAccountToken,
			Data: map[string][]byte{
				"ca.crt": serviceAccountCA(clusterName),
				"token":  []byte(fmt.Sprintf("token: %s", clusterName)),
			},
		}
		clientMap[clusterName] = common.NewKubeClientContainer(nil, fake.NewSimpleClientset(tokenSecret), nil)
	}

	if len(existingKubeConfig) > 0 {
		_, err := clientMap[flags.CentralCluster].CoreV1().Secrets(flags.CentralClusterNamespace).Create(context.Background(), &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      common.KubeConfigSecretName,
				Namespace: flags.CentralClusterNamespace,
				// the label the tool sets, so --cleanup deletes this Secret like the real one
				Labels: map[string]string{"multi-cluster": "true"},
			},
			Data: map[string][]byte{common.KubeConfigSecretKey: existingKubeConfig},
		}, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	return clientMap
}

// serviceAccountCA is the ca.crt k8s would write into that cluster's Service Account token Secret.
func serviceAccountCA(clusterName string) []byte {
	return []byte(fmt.Sprintf("ca.crt: %s", clusterName))
}

func generateCAPEM(t *testing.T, commonName string) []byte {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour * 24),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
