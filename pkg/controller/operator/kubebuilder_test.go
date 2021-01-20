package operator

import (
	"fmt"
	"os"
	"path"
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/pem"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"go.uber.org/zap"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/service"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/construct"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/container"

	enterprisestatefulset "github.com/10gen/ops-manager-kubernetes/pkg/kube/statefulset"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/podtemplatespec"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/kube/statefulset"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"

	"github.com/10gen/ops-manager-kubernetes/pkg/controller/operator/mock"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"

	"k8s.io/apimachinery/pkg/api/resource"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	mdb := DefaultReplicaSetBuilder().SetPersistent(nil).Build()
	set, err := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 4)

	mdb = DefaultReplicaSetBuilder().SetPersistent(util.BooleanRef(true)).Build()
	set, err = construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 4)

	// If no persistence is set then we still mount init scripts
	mdb = DefaultReplicaSetBuilder().SetPersistent(util.BooleanRef(false)).Build()
	set, err = construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 1)
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingle(t *testing.T) {
	labels := map[string]string{"app": "foo"}
	persistence := mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast").SetLabelSelector(labels)
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(persistence).Build().MongoDbPodSpec
	rs := DefaultReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec).Build()
	set, err := construct.DatabaseStatefulSet(*rs, construct.ReplicaSetOptions())

	assert.NoError(t, err)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), labels)})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData, SubPath: util.PvcNameData},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathJournal, SubPath: util.PvcNameJournal},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathLogs, SubPath: util.PvcNameLogs},
		{Name: construct.PvcNameDatabaseScripts, MountPath: construct.PvcMountPathScripts, ReadOnly: true},
	})
}

//TestBuildStatefulSet_PersistentVolumeClaimMultiple checks multiple volumes for multiple mounts. Note, that subpaths
//for mount points are not created (unlike in single mode)
func TestBuildStatefulSet_PersistentVolumeClaimMultiple(t *testing.T) {
	labels1 := map[string]string{"app": "bar"}
	labels2 := map[string]string{"app": "foo"}
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		mdbv1.NewPersistenceBuilder("3G").SetStorageClass("slow").SetLabelSelector(labels1),
		mdbv1.NewPersistenceBuilder("500M").SetStorageClass("fast").SetLabelSelector(labels2),
	).Build()

	mdb := DefaultReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set, err := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, "3G", stringutil.Ref("slow"), labels1),
		pvClaim(util.PvcNameLogs, "500M", stringutil.Ref("fast"), labels2),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: construct.PvcNameDatabaseScripts, MountPath: construct.PvcMountPathScripts, ReadOnly: true},
		{Name: util.PvcNameJournal, MountPath: util.PvcMountPathJournal},
		{Name: util.PvcNameLogs, MountPath: util.PvcMountPathLogs},
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults checks the scenario when storage is provided only for one
// mount point. Default values are expected to be used for two others
func TestBuildStatefulSet_PersistentVolumeClaimMultipleDefaults(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		nil,
		nil).
		Build()
	mdb := DefaultReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set, err := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, util.DefaultJournalStorageSize, nil, nil),
		pvClaim(util.PvcNameLogs, util.DefaultLogsStorageSize, nil, nil),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: construct.PvcNameDatabaseScripts, MountPath: construct.PvcMountPathScripts, ReadOnly: true},
		{Name: util.PvcNameJournal, MountPath: util.PvcMountPathJournal},
		{Name: util.PvcNameLogs, MountPath: util.PvcMountPathLogs},
	})
}

func TestBuildAppDbStatefulSetDefault(t *testing.T) {
	appDbSts, err := construct.AppDbStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.InitContainers, 1)
	assert.Equal(t, podSpecTemplate.InitContainers[0].Name, "mongodb-enterprise-init-appdb")
	assert.Len(t, podSpecTemplate.Containers, 1, "Should have only the db")
	assert.Equal(t, "mongodb-enterprise-appdb", podSpecTemplate.Containers[0].Name, "Database container should always be first")
}

type mockSecretGetter struct {
	secret *corev1.Secret
}

func (m mockSecretGetter) GetSecret(_ client.ObjectKey) (corev1.Secret, error) {
	if m.secret == nil {
		return corev1.Secret{}, fmt.Errorf("not found")
	}
	return *m.secret, nil
}

func TestReadPemHashFromSecret(t *testing.T) {

	name := "res-name"
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-cert", Namespace: mock.TestNamespace},
		Data:       map[string][]byte{"hello": []byte("world")},
	}

	assert.Empty(t, pem.ReadHashFromSecret(mockSecretGetter{}, mock.TestNamespace, name, zap.S()), "secret does not exist so pem hash should be empty")
	assert.NotEmpty(t, pem.ReadHashFromSecret(mockSecretGetter{secret: secret}, mock.TestNamespace, name, zap.S()), "pem hash should be read from the secret")
}

func TestBasePodSpec_Affinity(t *testing.T) {
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetNodeAffinity(nodeAffinity).
		SetPodAffinity(podAffinity).
		SetPodAntiAffinityTopologyKey("nodeId").
		Build()
	mdb := DefaultReplicaSetBuilder().
		SetName("s").
		SetPodSpec(&podSpec.MongoDbPodSpec).
		Build()
	sts, err := construct.DatabaseStatefulSet(
		*mdb, construct.ReplicaSetOptions(),
	)
	assert.NoError(t, err)

	spec := sts.Spec.Template.Spec
	assert.Equal(t, nodeAffinity, *spec.Affinity.NodeAffinity)
	assert.Equal(t, podAffinity, *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	assert.Len(t, spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution, 0)
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "s"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, "nodeId", term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_AntiAffinityDefaultTopology checks that the default topology key is created if the topology key is
// not specified
func TestBasePodSpec_AntiAffinityDefaultTopology(t *testing.T) {
	sts, err := construct.DatabaseStatefulSet(*DefaultStandaloneBuilder().SetName("my-standalone").Build(), construct.StandaloneOptions())
	assert.NoError(t, err)

	spec := sts.Spec.Template.Spec
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, int32(100), term.Weight)
	assert.Equal(t, map[string]string{PodAntiAffinityLabelKey: "my-standalone"}, term.PodAffinityTerm.LabelSelector.MatchLabels)
	assert.Equal(t, util.DefaultAntiAffinityTopologyKey, term.PodAffinityTerm.TopologyKey)
}

// TestBasePodSpec_ImagePullSecrets verifies that 'spec.ImagePullSecrets' is created only if env variable
// IMAGE_PULL_SECRETS is initialized
func TestBasePodSpec_ImagePullSecrets(t *testing.T) {
	// Cleaning the state (there is no tear down in go test :( )
	defer InitDefaultEnvVariables()

	sts, err := construct.DatabaseStatefulSet(*DefaultStandaloneBuilder().Build(), construct.StandaloneOptions())
	assert.NoError(t, err)

	template := sts.Spec.Template
	assert.Nil(t, template.Spec.ImagePullSecrets)

	_ = os.Setenv(util.ImagePullSecrets, "foo")

	sts, err = construct.DatabaseStatefulSet(*DefaultStandaloneBuilder().Build(), construct.StandaloneOptions())
	assert.NoError(t, err)

	template = sts.Spec.Template
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, template.Spec.ImagePullSecrets)

}

// TestBasePodSpec_TerminationGracePeriodSeconds verifies that the TerminationGracePeriodSeconds is set to 600 seconds
func TestBasePodSpec_TerminationGracePeriodSeconds(t *testing.T) {
	sts, err := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().Build(), construct.ReplicaSetOptions())
	assert.NoError(t, err)
	assert.Equal(t, util.Int64Ref(600), sts.Spec.Template.Spec.TerminationGracePeriodSeconds)
}

func checkPvClaims(t *testing.T, set appsv1.StatefulSet, expectedClaims []corev1.PersistentVolumeClaim) {
	assert.Len(t, set.Spec.VolumeClaimTemplates, len(expectedClaims))

	for i, c := range expectedClaims {
		assert.Equal(t, c, set.Spec.VolumeClaimTemplates[i])
	}
}
func checkMounts(t *testing.T, set appsv1.StatefulSet, expectedMounts []corev1.VolumeMount) {
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, len(expectedMounts))

	for i, c := range expectedMounts {
		assert.Equal(t, c, set.Spec.Template.Spec.Containers[0].VolumeMounts[i])
	}
}

func pvClaim(pvName, size string, storageClass *string, labels map[string]string) corev1.PersistentVolumeClaim {
	quantity, _ := resource.ParseQuantity(size)
	expectedClaim := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: pvName,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: quantity},
			},
			StorageClassName: storageClass,
		}}
	if len(labels) > 0 {
		expectedClaim.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	}
	return expectedClaim
}

func TestDefaultPodSpec_FsGroup(t *testing.T) {
	defer InitDefaultEnvVariables()

	sts, err := construct.DatabaseStatefulSet(*DefaultStandaloneBuilder().Build(), construct.StandaloneOptions())
	assert.NoError(t, err)

	spec := sts.Spec.Template.Spec
	assert.Len(t, spec.InitContainers, 1)
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	_ = os.Setenv(util.ManagedSecurityContextEnv, "true")

	sts, err = construct.DatabaseStatefulSet(*DefaultStandaloneBuilder().Build(), construct.StandaloneOptions())
	assert.NoError(t, err)
	assert.Nil(t, sts.Spec.Template.Spec.SecurityContext)
	// TODO: assert the container security context
}

func TestPodSpec_Requirements(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetCpuRequests("0.1").
		SetMemoryRequest("512M").
		SetCpu("0.3").
		SetMemory("1012M").
		Build()

	sts, err := construct.DatabaseStatefulSet(*DefaultReplicaSetBuilder().SetPodSpec(&podSpec.MongoDbPodSpec).Build(), construct.ReplicaSetOptions())
	assert.NoError(t, err)

	podSpecTemplate := sts.Spec.Template
	container := podSpecTemplate.Spec.Containers[0]
	expectedLimits := corev1.ResourceList{corev1.ResourceCPU: construct.ParseQuantityOrZero("0.3"), corev1.ResourceMemory: construct.ParseQuantityOrZero("1012M")}
	expectedRequests := corev1.ResourceList{corev1.ResourceCPU: construct.ParseQuantityOrZero("0.1"), corev1.ResourceMemory: construct.ParseQuantityOrZero("512M")}
	assert.Equal(t, expectedLimits, container.Resources.Limits)
	assert.Equal(t, expectedRequests, container.Resources.Requests)
}

func TestService_merge0(t *testing.T) {
	dst := corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"}}
	src := corev1.Service{}
	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)

	// Name and Namespace will not be copied over.
	src = corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "new-service", Namespace: "new-namespace"}}
	dst = service.Merge(dst, src)
	assert.Equal(t, "my-service", dst.ObjectMeta.Name)
	assert.Equal(t, "my-namespace", dst.ObjectMeta.Namespace)
}

func TestService_NodePortIsNotOverwritten(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsNotOverwrittenIfNoNodePortIsSpecified(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{NodePort: 30030}}},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{}}},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30030), dst.Spec.Ports[0].NodePort)
}

func TestService_NodePortIsKeptWhenChangingServiceType(t *testing.T) {
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30030}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}
	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30099}},
			Type:  corev1.ServiceTypeNodePort,
		},
	}
	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30099), dst.Spec.Ports[0].NodePort)

	src = corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "my-service", Namespace: "my-namespace"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{NodePort: 30011}},
			Type:  corev1.ServiceTypeLoadBalancer,
		},
	}

	dst = service.Merge(dst, src)
	assert.Equal(t, int32(30011), dst.Spec.Ports[0].NodePort)
}

func TestService_mergeAnnotations(t *testing.T) {
	// Annotations will be added
	annotationsDest := make(map[string]string)
	annotationsDest["annotation0"] = "value0"
	annotationsDest["annotation1"] = "value1"
	annotationsSrc := make(map[string]string)
	annotationsSrc["annotation0"] = "valueXXXX"
	annotationsSrc["annotation2"] = "value2"

	src := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: annotationsSrc,
		},
	}
	dst := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-service", Namespace: "my-namespace",
			Annotations: annotationsDest,
		},
	}
	dst = service.Merge(dst, src)
	assert.Len(t, dst.ObjectMeta.Annotations, 3)
	assert.Equal(t, dst.ObjectMeta.Annotations["annotation0"], "valueXXXX")
}

func TestBuildBackupDaemonContainer(t *testing.T) {
	sts, err := construct.BackupDaemonStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	template := sts.Spec.Template
	container := template.Spec.Containers[0]
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)

	assert.Equal(t, util.BackupDaemonContainerName, container.Name)
	assert.Nil(t, container.ReadinessProbe)

	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_backup_daemon"},
		container.Lifecycle.PreStop.Exec.Command)
}

// TestOpsManagerPodTemplate_Container verifies the default OM container built by 'opsManagerPodTemplate' method
func TestOpsManagerPodTemplate_Container(t *testing.T) {
	om := DefaultOpsManagerBuilder().Build()
	sts, err := construct.OpsManagerStatefulSet(om)
	assert.NoError(t, err)
	template := sts.Spec.Template

	assert.Len(t, template.Spec.Containers, 1)
	container := template.Spec.Containers[0]
	assert.Equal(t, util.OpsManagerContainerName, container.Name)
	// TODO change when we stop using versioning
	assert.Equal(t, "quay.io/mongodb/mongodb-enterprise-ops-manager:4.2.0", container.Image)
	assert.Equal(t, corev1.PullNever, container.ImagePullPolicy)

	assert.Equal(t, int32(util.OpsManagerDefaultPortHTTP), container.Ports[0].ContainerPort)
	assert.Equal(t, "/monitor/health", container.ReadinessProbe.Handler.HTTPGet.Path)
	assert.Equal(t, int32(8080), container.ReadinessProbe.Handler.HTTPGet.Port.IntVal)

	assert.Equal(t, []string{"/opt/scripts/docker-entry-point.sh"}, container.Command)
	assert.Equal(t, []string{"/bin/sh", "-c", "/mongodb-ops-manager/bin/mongodb-mms stop_mms"},
		container.Lifecycle.PreStop.Exec.Command)
}

func TestOpsManagerPodTemplate_ImagePullPolicy(t *testing.T) {
	defer InitDefaultEnvVariables()

	omSts, err := construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)

	podSpecTemplate := omSts.Spec.Template
	spec := podSpecTemplate.Spec

	assert.Nil(t, spec.ImagePullSecrets)

	os.Setenv(util.ImagePullSecrets, "my-cool-secret")
	omSts, err = construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	podSpecTemplate = omSts.Spec.Template
	spec = podSpecTemplate.Spec

	assert.NotNil(t, spec.ImagePullSecrets)
	assert.Equal(t, spec.ImagePullSecrets[0].Name, "my-cool-secret")
}

func TestOpsManagerPodTemplate_TerminationTimeout(t *testing.T) {
	omSts, err := construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	podSpecTemplate := omSts.Spec.Template
	assert.Equal(t, int64(300), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

func TestBackupPodTemplate_TerminationTimeout(t *testing.T) {
	set, err := construct.BackupDaemonStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	podSpecTemplate := set.Spec.Template
	assert.Equal(t, int64(4200), *podSpecTemplate.Spec.TerminationGracePeriodSeconds)
}

// TestOpsManagerPodTemplate_SecurityContext verifies that security context is created correctly
// in OpsManager/BackupDaemon podTemplate. It's not built if 'MANAGED_SECURITY_CONTEXT' env var
// is set to 'true'
func TestOpsManagerPodTemplate_SecurityContext(t *testing.T) {
	defer InitDefaultEnvVariables()

	omSts, err := construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)

	podSpecTemplate := omSts.Spec.Template
	spec := podSpecTemplate.Spec
	assert.Len(t, spec.InitContainers, 1)
	assert.Equal(t, spec.InitContainers[0].Name, "mongodb-enterprise-init-ops-manager")
	require.NotNil(t, spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)

	_ = os.Setenv(util.ManagedSecurityContextEnv, "true")

	omSts, err = construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	podSpecTemplate = omSts.Spec.Template
	assert.Nil(t, podSpecTemplate.Spec.SecurityContext)
}

func buildStatefulSetFromOpsManager(om omv1.MongoDBOpsManager) appsv1.StatefulSet {
	omSts, _ := construct.OpsManagerStatefulSet(om)
	return omSts
}

// TestOpsManagerPodTemplate_PodSpec verifies that StatefulSetSpec is applied correctly to OpsManager/Backup pod template.
func TestOpsManagerPodTemplate_PodSpec(t *testing.T) {
	omSts := buildStatefulSetFromOpsManager(DefaultOpsManagerBuilder().Build())
	resourceLimits := buildSafeResourceList("1.0", "500M")
	resourceRequests := buildSafeResourceList("0.5", "400M")
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	stsSpecOverride := appsv1.StatefulSetSpec{
		Template: podtemplatespec.New(
			podtemplatespec.WithAffinity(omSts.Name, PodAntiAffinityLabelKey, 100),
			podtemplatespec.WithPodAffinity(&podAffinity),
			podtemplatespec.WithNodeAffinity(&nodeAffinity),
			podtemplatespec.WithTopologyKey("rack", 0),
			podtemplatespec.WithContainer(util.OpsManagerContainerName,
				container.Apply(
					container.WithName(util.OpsManagerContainerName),
					container.WithResourceRequirements(corev1.ResourceRequirements{
						Limits:   resourceLimits,
						Requests: resourceRequests,
					}),
				),
			),
		),
	}
	mergedSts, err := enterprisestatefulset.MergeSpec(omSts, &stsSpecOverride)
	require.NoError(t, err)

	spec := mergedSts.Spec.Template.Spec
	assert.Equal(t, defaultNodeAffinity(), *spec.Affinity.NodeAffinity)
	assert.Equal(t, defaultPodAffinity(), *spec.Affinity.PodAffinity)
	assert.Len(t, spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution, 1)
	// the pod anti affinity term was overridden
	term := spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution[0]
	assert.Equal(t, "rack", term.PodAffinityTerm.TopologyKey)

	req := spec.Containers[0].Resources.Limits
	assert.Len(t, req, 2)
	cpu := req[corev1.ResourceCPU]
	memory := req[corev1.ResourceMemory]
	assert.Equal(t, "1", (&cpu).String())
	assert.Equal(t, int64(500000000), (&memory).Value())

	req = spec.Containers[0].Resources.Requests
	assert.Len(t, req, 2)
	cpu = req[corev1.ResourceCPU]
	memory = req[corev1.ResourceMemory]
	assert.Equal(t, "500m", (&cpu).String())
	assert.Equal(t, int64(400000000), (&memory).Value())
}

// TestOpsManagerPodTemplate_MergePodTemplate checks the custom pod template provided by the user.
// It's supposed to override the values produced by the Operator and leave everything else as is
func TestOpsManagerPodTemplate_MergePodTemplate(t *testing.T) {
	expectedAnnotations := map[string]string{"customKey": "customVal", "connectionStringHash": ""}
	expectedTolerations := []corev1.Toleration{{Key: "dedicated", Value: "database"}}
	newContainer := corev1.Container{
		Name:  "my-custom-container",
		Image: "my-custom-image",
	}

	podTemplateSpec := podtemplatespec.New(
		podtemplatespec.WithAnnotations(expectedAnnotations),
		podtemplatespec.WithServiceAccount("test-account"),
		podtemplatespec.WithTolerations(expectedTolerations),
		podtemplatespec.WithContainer("my-custom-container",
			container.Apply(
				container.WithName("my-custom-container"),
				container.WithImage("my-custom-image"),
			)),
	)

	om := DefaultOpsManagerBuilder().Build()
	template := buildStatefulSetFromOpsManager(om).Spec.Template
	originalLabels := template.Labels

	operatorSts := appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: template,
		},
	}

	mergedSts, err := enterprisestatefulset.MergeSpec(operatorSts, &appsv1.StatefulSetSpec{
		Template: podTemplateSpec,
	})
	assert.NoError(t, err)
	template = mergedSts.Spec.Template
	// Service account gets overriden by custom pod template
	assert.Equal(t, "test-account", template.Spec.ServiceAccountName)
	assert.Equal(t, expectedAnnotations, template.Annotations)
	assert.Equal(t, expectedTolerations, template.Spec.Tolerations)
	assert.Len(t, template.Spec.Containers, 2)
	assert.Equal(t, newContainer, template.Spec.Containers[1])

	// Some validation that the Operator-made config hasn't suffered
	assert.Equal(t, originalLabels, template.Labels)
	require.NotNil(t, template.Spec.SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), template.Spec.SecurityContext.FSGroup)
	assert.Equal(t, util.OpsManagerContainerName, template.Spec.Containers[0].Name)
}

func Test_buildOpsManagerStatefulSet(t *testing.T) {
	sts, err := construct.OpsManagerStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	assert.Equal(t, "test-om", sts.ObjectMeta.Name)
	assert.Equal(t, util.OpsManagerContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.Equal(t, []string{"/opt/scripts/docker-entry-point.sh"},
		sts.Spec.Template.Spec.Containers[0].Command)
}

func Test_buildBackupDaemonStatefulSet(t *testing.T) {
	sts, err := construct.BackupDaemonStatefulSet(DefaultOpsManagerBuilder().Build())
	assert.NoError(t, err)
	assert.Equal(t, "test-om-backup-daemon", sts.ObjectMeta.Name)
	assert.Equal(t, util.BackupDaemonContainerName, sts.Spec.Template.Spec.Containers[0].Name)
	assert.Nil(t, sts.Spec.Template.Spec.Containers[0].ReadinessProbe)
}

func TestBuildOpsManagerStatefulSet(t *testing.T) {
	t.Run("Env Vars Sorted", func(t *testing.T) {
		om := DefaultOpsManagerBuilder().
			AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
			AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
			Build()

		sts, err := construct.OpsManagerStatefulSet(om)
		assert.NoError(t, err)

		// env vars are in sorted order
		expectedVars := []corev1.EnvVar{
			{Name: "OM_PROP_mms_adminEmailAddr", Value: "cloud-manager-support@mongodb.com"},
			{Name: "OM_PROP_mms_centralUrl", Value: "http://om-svc"},
			env.FromSecret("OM_PROP_mongo_mongoUri", om.AppDBMongoConnectionStringSecretName(), "connectionString"),
			{Name: "CUSTOM_JAVA_MMS_UI_OPTS", Value: "-Xmx4291m -Xms4291m"},
			{Name: "CUSTOM_JAVA_DAEMON_OPTS", Value: "-Xmx4291m -Xms4291m"},
		}
		env := sts.Spec.Template.Spec.Containers[0].Env
		assert.Equal(t, expectedVars, env)
	})
	t.Run("JVM params applied after StatefulSet merge", func(t *testing.T) {
		requirements := corev1.ResourceRequirements{
			Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("6G")},
			Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("400M")},
		}

		statefulSet := statefulset.New(
			statefulset.WithPodSpecTemplate(
				podtemplatespec.Apply(
					podtemplatespec.WithContainer(util.OpsManagerContainerName,
						container.Apply(
							container.WithName(util.OpsManagerContainerName),
							container.WithResourceRequirements(requirements),
						),
					),
				),
			),
		)

		om := DefaultOpsManagerBuilder().
			SetStatefulSetSpec(statefulSet.Spec).
			Build()

		sts, err := construct.OpsManagerStatefulSet(om)
		assert.NoError(t, err)
		expectedVars := []corev1.EnvVar{
			env.FromSecret("OM_PROP_mongo_mongoUri", om.AppDBMongoConnectionStringSecretName(), "connectionString"),
			{Name: "CUSTOM_JAVA_MMS_UI_OPTS", Value: "-Xmx5149m -Xms343m"},
			{Name: "CUSTOM_JAVA_DAEMON_OPTS", Value: "-Xmx5149m -Xms343m"},
		}
		env := sts.Spec.Template.Spec.Containers[0].Env
		assert.Equal(t, expectedVars, env)
	})

}

func TestBuildJvmParamsEnvVars_FromDefaultPodSpec(t *testing.T) {
	om := DefaultOpsManagerBuilder().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	template := buildStatefulSetFromOpsManager(om).Spec.Template

	envVar, err := construct.BuildJvmParamsEnvVars(om.Spec, template)
	assert.NoError(t, err)
	// xmx and xms based calculated from  default container memory, requests.mem=limits.mem=5GB
	assert.Equal(t, "CUSTOM_JAVA_MMS_UI_OPTS", envVar[0].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m", envVar[0].Value)

	assert.Equal(t, "CUSTOM_JAVA_DAEMON_OPTS", envVar[1].Name)
	assert.Equal(t, "-Xmx4291m -Xms4291m", envVar[1].Value)
}

func TestBuildJvmParamsEnvVars_FromCustomContainerResource(t *testing.T) {
	om := DefaultOpsManagerBuilder().
		AddConfiguration(util.MmsCentralUrlPropKey, "http://om-svc").
		AddConfiguration("mms.adminEmailAddr", "cloud-manager-support@mongodb.com").
		Build()
	om.Spec.JVMParams = []string{"-DFakeOptionEnabled"}

	template := buildStatefulSetFromOpsManager(om).Spec.Template

	unsetQuantity := *resource.NewQuantity(0, resource.BinarySI)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = *resource.NewQuantity(268435456, resource.BinarySI)
	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarLimitsOnly, err := construct.BuildJvmParamsEnvVars(om.Spec, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = *resource.NewQuantity(218435456, resource.BinarySI)
	envVarLimitsAndReqs, err := construct.BuildJvmParamsEnvVars(om.Spec, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Limits[corev1.ResourceMemory] = unsetQuantity
	envVarReqsOnly, err := construct.BuildJvmParamsEnvVars(om.Spec, template)
	assert.NoError(t, err)

	template.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = unsetQuantity
	envVarsNoLimitsOrReqs, err := construct.BuildJvmParamsEnvVars(om.Spec, template)
	assert.NoError(t, err)

	// if only memory requests are configured, xms and xmx should be 90% of mem request
	assert.Equal(t, "-DFakeOptionEnabled -Xmx187m -Xms187m", envVarReqsOnly[0].Value)
	// both are configured, xms should be 90% of mem request, and xmx 90% of mem limit
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms187m", envVarLimitsAndReqs[0].Value)
	// if only memory limits are configured, xms and xmx should be 90% of mem limits
	assert.Equal(t, "-DFakeOptionEnabled -Xmx230m -Xms230m", envVarLimitsOnly[0].Value)
	// if neither is configured, xms/xmx params should not be added to JVM params, keeping OM defaults
	assert.Equal(t, "-DFakeOptionEnabled", envVarsNoLimitsOrReqs[0].Value)

}

func TestBaseEnvHelper(t *testing.T) {
	envVars := defaultPodVars()
	podEnv := databaseEnvVars(envVars)
	assert.Len(t, podEnv, 5)

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	podEnv = databaseEnvVars(envVars)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})

	envVars = defaultPodVars()
	envVars.SSLMMSCAConfigMap = "custom-ca"
	trustedCACertLocation := path.Join(CaCertMountPath, util.CaCertMMS)
	podEnv = databaseEnvVars(envVars)
	assert.Len(t, podEnv, 6)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})

	envVars = defaultPodVars()
	envVars.SSLRequireValidMMSServerCertificates = true
	envVars.SSLMMSCAConfigMap = "custom-ca"
	podEnv = databaseEnvVars(envVars)
	assert.Len(t, podEnv, 7)
	assert.Equal(t, podEnv[5], corev1.EnvVar{
		Name:  util.EnvVarSSLRequireValidMMSCertificates,
		Value: "true",
	})
	assert.Equal(t, podEnv[6], corev1.EnvVar{
		Name:  util.EnvVarSSLTrustedMMSServerCertificate,
		Value: trustedCACertLocation,
	})
}

func TestAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	mdb := mdbv1.NewReplicaSetBuilder().SetAgentConfig(mdbv1.AgentConfig{StartupParameters: agentStartupParameters}).Build()
	sts, err := construct.DatabaseStatefulSet(*mdb, construct.ReplicaSetOptions())
	assert.NoError(t, err)
	variablesMap := envVariablesAsMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")

}

func TestAppDBAgentFlags(t *testing.T) {
	agentStartupParameters := mdbv1.StartupParameters{
		"Key1": "Value1",
		"Key2": "Value2",
	}

	om := DefaultOpsManagerBuilder().Build()
	om.Spec.AppDB.MongoDbSpec.Agent.StartupParameters = agentStartupParameters
	sts, err := construct.AppDbStatefulSet(om)
	assert.NoError(t, err)

	variablesMap := envVariablesAsMap(sts.Spec.Template.Spec.Containers[0].Env...)
	val, ok := variablesMap["AGENT_FLAGS"]
	assert.True(t, ok)
	assert.Contains(t, val, "-Key1,Value1", "-Key2,Value2")
}

// ******************************** Helper methods *******************************************

func defaultPodVars() *env.PodEnvVars {
	return &env.PodEnvVars{BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}

func defaultNodeAffinity() corev1.NodeAffinity {
	return corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{{
				MatchExpressions: []corev1.NodeSelectorRequirement{{
					Key:    "dc",
					Values: []string{"US-EAST"},
				}}},
			}},
	}
}
func defaultPodAffinity() corev1.PodAffinity {
	return corev1.PodAffinity{
		PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{{
			Weight: 50,
			PodAffinityTerm: corev1.PodAffinityTerm{
				LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web-server"}},
				TopologyKey:   "rack",
			},
		}}}
}

// buildSafeResourceList returns a ResourceList but should not be called
// with dynamic values. This function ignores errors in the parsing of
// resource.Quantities and as a result should only be used in tests with
// pre-set valid values.
func buildSafeResourceList(cpu, memory string) corev1.ResourceList {
	res := corev1.ResourceList{}
	if q := construct.ParseQuantityOrZero(cpu); !q.IsZero() {
		res[corev1.ResourceCPU] = q
	}
	if q := construct.ParseQuantityOrZero(memory); !q.IsZero() {
		res[corev1.ResourceMemory] = q
	}
	return res
}

func envVariablesAsMap(vars ...corev1.EnvVar) map[string]string {
	variablesMap := map[string]string{}
	for _, envVar := range vars {
		variablesMap[envVar.Name] = envVar.Value
	}
	return variablesMap
}
