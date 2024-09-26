package construct

import (
	"testing"

	"github.com/10gen/ops-manager-kubernetes/pkg/multicluster"

	"github.com/10gen/ops-manager-kubernetes/pkg/util/architectures"

	omv1 "github.com/10gen/ops-manager-kubernetes/api/v1/om"

	"github.com/10gen/ops-manager-kubernetes/controllers/operator/construct/scalers"
	"github.com/10gen/ops-manager-kubernetes/controllers/operator/mock"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdb"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/env"
	"github.com/10gen/ops-manager-kubernetes/pkg/util/stringutil"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildStatefulSet_PersistentFlagStatic(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 7)
	assert.Len(t, set.Spec.Template.Spec.Containers[1].VolumeMounts, 7)

	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(true)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 7)
	assert.Len(t, set.Spec.Template.Spec.Containers[1].VolumeMounts, 7)

	// If no persistence is set then we still mount init scripts
	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(false)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 7)
	assert.Len(t, set.Spec.Template.Spec.Containers[1].VolumeMounts, 7)
}

func TestBuildStatefulSet_PersistentFlag(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))

	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 8)

	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(true)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 1)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 8)

	// If no persistence is set then we still mount init scripts
	mdb = mdbv1.NewReplicaSetBuilder().SetPersistent(util.BooleanRef(false)).Build()
	set = DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	assert.Len(t, set.Spec.VolumeClaimTemplates, 0)
	assert.Len(t, set.Spec.Template.Spec.Containers[0].VolumeMounts, 8)
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingle(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.NonStatic))

	labels := map[string]string{"app": "foo"}
	persistence := mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast").SetLabelSelector(labels)
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(persistence).Build().MongoDbPodSpec
	rs := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec).Build()
	set := DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), nil)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), labels)})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvMms, MountPath: util.PvcMmsHomeMountPath, SubPath: util.PvcMmsHome},
		{Name: util.PvMms, MountPath: util.PvcMountPathTmp, SubPath: util.PvcNameTmp},
		{Name: util.PvMms, MountPath: util.PvcMmsMountPath, SubPath: util.PvcMms},
		{Name: AgentAPIKeyVolumeName, MountPath: AgentAPIKeySecretPath},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData, SubPath: util.PvcNameData},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathJournal, SubPath: util.PvcNameJournal},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathLogs, SubPath: util.PvcNameLogs},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimSingle checks that one persistent volume claim is created that is mounted by
// 3 points
func TestBuildStatefulSet_PersistentVolumeClaimSingleStatic(t *testing.T) {
	t.Setenv(architectures.DefaultEnvArchitecture, string(architectures.Static))

	labels := map[string]string{"app": "foo"}
	persistence := mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast").SetLabelSelector(labels)
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetSinglePersistence(persistence).Build().MongoDbPodSpec
	rs := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec).Build()
	set := DatabaseStatefulSet(*rs, ReplicaSetOptions(GetPodEnvOptions()), nil)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), labels)})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvMms, MountPath: util.PvcMmsHomeMountPath, SubPath: util.PvcMmsHome},
		{Name: util.PvMms, MountPath: util.PvcMountPathTmp, SubPath: util.PvcNameTmp},
		{Name: util.PvMms, MountPath: util.PvcMmsMountPath, SubPath: util.PvcMms},
		{Name: AgentAPIKeyVolumeName, MountPath: AgentAPIKeySecretPath},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData, SubPath: util.PvcNameData},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathJournal, SubPath: util.PvcNameJournal},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathLogs, SubPath: util.PvcNameLogs},
	})
}

// TestBuildStatefulSet_PersistentVolumeClaimMultiple checks multiple volumes for multiple mounts. Note, that subpaths
// for mount points are not created (unlike in single mode)
func TestBuildStatefulSet_PersistentVolumeClaimMultiple(t *testing.T) {
	labels1 := map[string]string{"app": "bar"}
	labels2 := map[string]string{"app": "foo"}
	podSpec := mdbv1.NewPodSpecWrapperBuilder().SetMultiplePersistence(
		mdbv1.NewPersistenceBuilder("40G").SetStorageClass("fast"),
		mdbv1.NewPersistenceBuilder("3G").SetStorageClass("slow").SetLabelSelector(labels1),
		mdbv1.NewPersistenceBuilder("500M").SetStorageClass("fast").SetLabelSelector(labels2),
	).Build()

	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, "3G", stringutil.Ref("slow"), labels1),
		pvClaim(util.PvcNameLogs, "500M", stringutil.Ref("fast"), labels2),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvMms, MountPath: util.PvcMmsHomeMountPath, SubPath: util.PvcMmsHome},
		{Name: util.PvMms, MountPath: util.PvcMountPathTmp, SubPath: util.PvcNameTmp},
		{Name: util.PvMms, MountPath: util.PvcMmsMountPath, SubPath: util.PvcMms},
		{Name: AgentAPIKeyVolumeName, MountPath: AgentAPIKeySecretPath},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
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
	mdb := mdbv1.NewReplicaSetBuilder().SetPersistent(nil).SetPodSpec(&podSpec.MongoDbPodSpec).Build()
	set := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)

	checkPvClaims(t, set, []corev1.PersistentVolumeClaim{
		pvClaim(util.PvcNameData, "40G", stringutil.Ref("fast"), nil),
		pvClaim(util.PvcNameJournal, util.DefaultJournalStorageSize, nil, nil),
		pvClaim(util.PvcNameLogs, util.DefaultLogsStorageSize, nil, nil),
	})

	checkMounts(t, set, []corev1.VolumeMount{
		{Name: util.PvMms, MountPath: util.PvcMmsHomeMountPath, SubPath: util.PvcMmsHome},
		{Name: util.PvMms, MountPath: util.PvcMountPathTmp, SubPath: util.PvcNameTmp},
		{Name: util.PvMms, MountPath: util.PvcMmsMountPath, SubPath: util.PvcMms},
		{Name: AgentAPIKeyVolumeName, MountPath: AgentAPIKeySecretPath},
		{Name: util.PvcNameData, MountPath: util.PvcMountPathData},
		{Name: PvcNameDatabaseScripts, MountPath: PvcMountPathScripts, ReadOnly: true},
		{Name: util.PvcNameJournal, MountPath: util.PvcMountPathJournal},
		{Name: util.PvcNameLogs, MountPath: util.PvcMountPathLogs},
	})
}

func TestBuildAppDbStatefulSetDefault(t *testing.T) {
	om := omv1.NewOpsManagerBuilderDefault().Build()
	scaler := scalers.GetAppDBScaler(om, multicluster.LegacyCentralClusterName, 0, nil)
	appDbSts, err := AppDbStatefulSet(*om, &env.PodEnvVars{ProjectID: "abcd"}, AppDBStatefulSetOptions{}, scaler, appsv1.OnDeleteStatefulSetStrategyType, nil)
	assert.NoError(t, err)
	podSpecTemplate := appDbSts.Spec.Template.Spec
	assert.Len(t, podSpecTemplate.InitContainers, 1)
	assert.Len(t, podSpecTemplate.Containers, 2, "Should contain mongodb and agent")
	assert.Equal(t, "mongodb-agent", podSpecTemplate.Containers[0].Name)
	assert.Equal(t, "mongod", podSpecTemplate.Containers[1].Name)
}

func TestBasePodSpec_Affinity(t *testing.T) {
	nodeAffinity := defaultNodeAffinity()
	podAffinity := defaultPodAffinity()

	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetNodeAffinity(nodeAffinity).
		SetPodAffinity(podAffinity).
		SetPodAntiAffinityTopologyKey("nodeId").
		Build()
	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("s").
		SetPodSpec(&podSpec.MongoDbPodSpec).
		Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)

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
	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().SetName("my-standalone").Build(), StandaloneOptions(GetPodEnvOptions()), nil)

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
	defer mock.InitDefaultEnvVariables()

	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions(GetPodEnvOptions()), nil)

	template := sts.Spec.Template
	assert.Nil(t, template.Spec.ImagePullSecrets)

	t.Setenv(util.ImagePullSecrets, "foo")

	sts = DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions(GetPodEnvOptions()), nil)

	template = sts.Spec.Template
	assert.Equal(t, []corev1.LocalObjectReference{{Name: "foo"}}, template.Spec.ImagePullSecrets)
}

// TestBasePodSpec_TerminationGracePeriodSeconds verifies that the TerminationGracePeriodSeconds is set to 600 seconds
func TestBasePodSpec_TerminationGracePeriodSeconds(t *testing.T) {
	sts := DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().Build(), ReplicaSetOptions(GetPodEnvOptions()), nil)
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
		},
	}
	if len(labels) > 0 {
		expectedClaim.Spec.Selector = &metav1.LabelSelector{MatchLabels: labels}
	}
	return expectedClaim
}

func TestDefaultPodSpec_SecurityContext(t *testing.T) {
	defer mock.InitDefaultEnvVariables()

	sts := DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions(GetPodEnvOptions()), nil)

	spec := sts.Spec.Template.Spec
	assert.Len(t, spec.InitContainers, 1)
	assert.NotNil(t, spec.SecurityContext)
	assert.NotNil(t, spec.InitContainers[0].SecurityContext)
	assert.Equal(t, util.Int64Ref(util.FsGroup), spec.SecurityContext.FSGroup)
	assert.Equal(t, util.Int64Ref(util.RunAsUser), spec.SecurityContext.RunAsUser)
	assert.Equal(t, util.BooleanRef(true), spec.SecurityContext.RunAsNonRoot)
	assert.Equal(t, util.BooleanRef(true), spec.InitContainers[0].SecurityContext.ReadOnlyRootFilesystem)
	assert.Equal(t, util.BooleanRef(false), spec.InitContainers[0].SecurityContext.AllowPrivilegeEscalation)

	t.Setenv(util.ManagedSecurityContextEnv, "true")

	sts = DatabaseStatefulSet(*mdbv1.NewStandaloneBuilder().Build(), StandaloneOptions(GetPodEnvOptions()), nil)
	assert.Nil(t, sts.Spec.Template.Spec.SecurityContext)
}

func TestPodSpec_Requirements(t *testing.T) {
	podSpec := mdbv1.NewPodSpecWrapperBuilder().
		SetCpuRequests("0.1").
		SetMemoryRequest("512M").
		SetCpuLimit("0.3").
		SetMemoryLimit("1012M").
		Build()

	sts := DatabaseStatefulSet(*mdbv1.NewReplicaSetBuilder().SetPodSpec(&podSpec.MongoDbPodSpec).Build(), ReplicaSetOptions(GetPodEnvOptions()), nil)

	podSpecTemplate := sts.Spec.Template
	container := podSpecTemplate.Spec.Containers[0]

	expectedLimits := corev1.ResourceList{corev1.ResourceCPU: ParseQuantityOrZero("0.3"), corev1.ResourceMemory: ParseQuantityOrZero("1012M")}
	expectedRequests := corev1.ResourceList{corev1.ResourceCPU: ParseQuantityOrZero("0.1"), corev1.ResourceMemory: ParseQuantityOrZero("512M")}
	assert.Equal(t, expectedLimits, container.Resources.Limits)
	assert.Equal(t, expectedRequests, container.Resources.Requests)
}

func defaultPodVars() *env.PodEnvVars {
	return &env.PodEnvVars{BaseURL: "http://localhost:8080", ProjectID: "myProject", User: "user@some.com"}
}

func TestPodAntiAffinityOverride(t *testing.T) {
	podAntiAffinity := defaultPodAntiAffinity()

	podSpec := mdbv1.NewPodSpecWrapperBuilder().Build()

	// override pod Anti Affinity
	podSpec.PodTemplateWrapper.PodTemplate.Spec.Affinity.PodAntiAffinity = &podAntiAffinity

	mdb := mdbv1.NewReplicaSetBuilder().
		SetName("s").
		SetPodSpec(&podSpec.MongoDbPodSpec).
		Build()
	sts := DatabaseStatefulSet(*mdb, ReplicaSetOptions(GetPodEnvOptions()), nil)
	spec := sts.Spec.Template.Spec
	assert.Equal(t, podAntiAffinity, *spec.Affinity.PodAntiAffinity)
}
