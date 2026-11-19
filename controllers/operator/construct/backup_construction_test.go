package construct

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/mock"
	enterprisepem "github.com/mongodb/mongodb-kubernetes/controllers/operator/pem"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/secrets"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube/probes"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/vault"
)

func TestBuildBackupDaemonStatefulSet(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	assert.Equal(t, "test-om-backup-daemon", sts.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.NotNil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
}

func TestManagedQueryableBackupConstruction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		managed bool
		want    string
	}{
		{name: "managed", enabled: true, managed: true, want: "test-om-managed-backup-pem"},
		{name: "BYO", enabled: true, want: "byo-queryable"},
		{name: "disabled managed", managed: true},
		{name: "disabled BYO", want: "byo-queryable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			om := omv1.NewOpsManagerBuilderDefault().SetName("test-om").SetNamespace(mock.TestNamespace).Build()
			om.Spec.Backup.Enabled = tc.enabled
			if tc.managed {
				om.Spec.Backup.ManagedCertificate = &omv1.ManagedCertificate{}
			} else {
				om.Spec.Backup.QueryableBackupSecretRef.Name = "byo-queryable"
			}
			c, _ := mock.NewDefaultFakeClient(
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "test-om-managed-backup-pem", Namespace: mock.TestNamespace}, Data: map[string][]byte{"queryable.pem": []byte("managed PEM")}},
				&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "byo-queryable", Namespace: mock.TestNamespace}, Data: map[string][]byte{"queryable.pem": []byte("BYO PEM")}},
			)
			sc := secrets.SecretClient{KubeClient: c}
			mc := multicluster.GetLegacyCentralMemberCluster(1, 0, c, sc)
			for name, builder := range map[string]func() (appsv1.StatefulSet, error){
				"OM":     func() (appsv1.StatefulSet, error) { return OpsManagerStatefulSet(ctx, sc, om, mc, zap.S()) },
				"backup": func() (appsv1.StatefulSet, error) { return BackupDaemonStatefulSet(ctx, sc, om, mc, zap.S()) },
			} {
				t.Run(name, func(t *testing.T) {
					sts, err := builder()
					require.NoError(t, err)
					var secretName string
					for _, v := range sts.Spec.Template.Spec.Volumes {
						if v.Name == "queryable-pem" {
							require.NotNil(t, v.Secret)
							secretName = v.Secret.SecretName
						}
					}
					assert.Equal(t, tc.want, secretName)
					if tc.want != "" {
						assert.Contains(t, sts.Spec.Template.Spec.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "queryable-pem", MountPath: "/certs/", ReadOnly: true})
					}
					// Only an operator-managed queryable.pem rolls the pods when it changes.
					pemHash, annotated := sts.Spec.Template.Annotations[queryableBackupPemHashAnnotation]
					if tc.managed && tc.enabled {
						assert.Equal(t, enterprisepem.HashBytes([]byte("managed PEM")), pemHash)
					} else {
						assert.False(t, annotated)
					}
					assert.False(t, om.IsTLSEnabled())
				})
			}
		})
	}
}

// TestAppDBCABundleHashAnnotation checks that the operator-managed AppDB CA bundle hash rolls both
// the Ops Manager and the backup daemon pods: they load the AppDB CA only at startup. Without a
// managed bundle the pod template must not change.
func TestAppDBCABundleHashAnnotation(t *testing.T) {
	ctx := t.Context()
	om := omv1.NewOpsManagerBuilderDefault().SetName("test-om").SetNamespace(mock.TestNamespace).Build()
	om.Spec.Backup.Enabled = true
	c, _ := mock.NewDefaultFakeClient()
	sc := secrets.SecretClient{KubeClient: c}
	mc := multicluster.GetLegacyCentralMemberCluster(1, 0, c, sc)

	for name, builder := range map[string]func(...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error){
		"OM": func(opts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
			return OpsManagerStatefulSet(ctx, sc, om, mc, zap.S(), opts...)
		},
		"backup": func(opts ...func(*OpsManagerStatefulSetOptions)) (appsv1.StatefulSet, error) {
			return BackupDaemonStatefulSet(ctx, sc, om, mc, zap.S(), opts...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			sts, err := builder(WithAppDBCABundleHash("hash-one"))
			require.NoError(t, err)
			assert.Equal(t, "hash-one", sts.Spec.Template.Annotations[AppDBCABundleHashAnnotation])

			widened, err := builder(WithAppDBCABundleHash("hash-two"))
			require.NoError(t, err)
			assert.Equal(t, "hash-two", widened.Spec.Template.Annotations[AppDBCABundleHashAnnotation])

			unmanaged, err := builder(WithAppDBCABundleHash(""))
			require.NoError(t, err)
			assert.NotContains(t, unmanaged.Spec.Template.Annotations, AppDBCABundleHashAnnotation)
		})
	}
}

// TestManagedQueryableBackupPEMChangeRollsPods guards the renewal and CA rotation of the managed
// queryable backup certificate: Ops Manager loads queryable.pem into its queryable proxy only at
// startup, so a new queryable.pem must change the pod template of both StatefulSets.
func TestManagedQueryableBackupPEMChangeRollsPods(t *testing.T) {
	ctx := t.Context()
	om := omv1.NewOpsManagerBuilderDefault().SetName("test-om").SetNamespace(mock.TestNamespace).Build()
	om.Spec.Backup.Enabled = true
	om.Spec.Backup.ManagedCertificate = &omv1.ManagedCertificate{}
	pemSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "test-om-managed-backup-pem", Namespace: mock.TestNamespace}, Data: map[string][]byte{"queryable.pem": []byte("leaf, key and CA")}}
	c, _ := mock.NewDefaultFakeClient(pemSecret)
	sc := secrets.SecretClient{KubeClient: c}
	mc := multicluster.GetLegacyCentralMemberCluster(1, 0, c, sc)
	builders := map[string]func() (appsv1.StatefulSet, error){
		"OM":     func() (appsv1.StatefulSet, error) { return OpsManagerStatefulSet(ctx, sc, om, mc, zap.S()) },
		"backup": func() (appsv1.StatefulSet, error) { return BackupDaemonStatefulSet(ctx, sc, om, mc, zap.S()) },
	}
	templates := func() map[string]corev1.PodTemplateSpec {
		result := map[string]corev1.PodTemplateSpec{}
		for name, builder := range builders {
			sts, err := builder()
			require.NoError(t, err)
			result[name] = sts.Spec.Template
		}
		return result
	}

	before := templates()
	unchanged := templates()
	pemSecret.Data["queryable.pem"] = []byte("renewed leaf, key and CA")
	require.NoError(t, c.Update(ctx, pemSecret))
	after := templates()

	for name := range builders {
		assert.Equal(t, before[name], unchanged[name], "%s: an unchanged queryable.pem must not change the pod template", name)
		assert.NotEqual(t, before[name].Annotations[queryableBackupPemHashAnnotation], after[name].Annotations[queryableBackupPemHashAnnotation], "%s: a new queryable.pem must roll the pods", name)
		assert.Equal(t, enterprisepem.HashBytes([]byte("renewed leaf, key and CA")), after[name].Annotations[queryableBackupPemHashAnnotation], name)
	}
}

// TestBackupDaemonProbesRemainExec guards the backup daemon against the shared Ops Manager
// options now carrying a probe Scheme: the daemon's probes are exec-based and must stay that way.
func TestBackupDaemonProbesRemainExec(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "om-tls-cert", Namespace: mock.TestNamespace},
		Type:       corev1.SecretTypeOpaque,
	})
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	om := omv1.NewOpsManagerBuilderDefault().
		SetName("test-om").
		SetNamespace(mock.TestNamespace).
		SetTLSConfig(omv1.MongoDBOpsManagerTLS{
			SecretRef: omv1.TLSSecretRef{Name: "om-tls-cert"},
		}).
		Build()

	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, om, multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	require.NoError(t, err)
	require.Len(t, sts.Spec.Template.Spec.Containers, 1)
	containerObj := sts.Spec.Template.Spec.Containers[0]

	for probeName, probe := range map[string]*corev1.Probe{
		"readiness": containerObj.ReadinessProbe,
		"liveness":  containerObj.LivenessProbe,
		"startup":   containerObj.StartupProbe,
	} {
		require.NotNil(t, probe, "%s probe must be set", probeName)
		assert.Nil(t, probe.HTTPGet, "%s probe must not use an HTTPGet handler", probeName)
		require.NotNil(t, probe.Exec, "%s probe must use an Exec handler", probeName)
	}
}

func TestBackupPodTemplate_TerminationTimeout(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	set, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	podSpecTemplate := set.Spec.Template
	assert.Equal(t, int64(4200), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

func TestBuildBackupDaemonContainer(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S(),
		WithOpsManagerImage("quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0"),
	)
	assert.NoError(t, err)
	template := sts.Spec.Template
	container := template.Spec.Containers[0]
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)

	assert.Equal(t, util.BackupDaemonContainerName, container.Name)

	expectedProbe := probes.New(buildBackupDaemonReadinessProbe())
	assert.Equal(t, &expectedProbe, container.ReadinessProbe)

	expectedProbe = probes.New(buildBackupDaemonLivenessProbe())
	assert.Equal(t, &expectedProbe, container.LivenessProbe)

	expectedProbe = probes.New(buildBackupDaemonStartupProbe())
	assert.Equal(t, &expectedProbe, container.StartupProbe)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
		container.Lifecycle.PreStop.Exec.Command)
}

func TestMultipleBackupDaemons(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}
	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, omv1.NewOpsManagerBuilderDefault().SetVersion("4.2.0").SetBackupMembers(3).Build(), multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)
	assert.Equal(t, 3, int(*sts.Spec.Replicas))
}

// TestBackupDaemonStatefulSet_LabelsNotPropagatedToVolumeClaimTemplates asserts that the Ops Manager
// resource labels reach the StatefulSet metadata but not the head DB volume claim template, which is
// immutable and would wedge the StatefulSet on every label change (CLOUDP-208587).
func TestBackupDaemonStatefulSet_LabelsNotPropagatedToVolumeClaimTemplates(t *testing.T) {
	ctx := context.Background()
	client, _ := mock.NewDefaultFakeClient()
	secretsClient := secrets.SecretClient{
		VaultClient: &vault.VaultClient{},
		KubeClient:  client,
	}

	opsManager := omv1.NewOpsManagerBuilderDefault().SetName("test-om").Build()
	opsManager.Labels = map[string]string{"label1": "val1"}

	sts, err := BackupDaemonStatefulSet(ctx, secretsClient, opsManager, multicluster.GetLegacyCentralMemberCluster(1, 0, client, secretsClient), zap.S())
	assert.NoError(t, err)

	assert.Equal(t, "val1", sts.Labels["label1"])
	assert.Len(t, sts.Spec.VolumeClaimTemplates, 1)
	assert.Empty(t, sts.Spec.VolumeClaimTemplates[0].Labels)
}
