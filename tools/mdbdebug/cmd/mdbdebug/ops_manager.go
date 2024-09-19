package main

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"golang.org/x/xerrors"
	"io"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/v1/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/configmap"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
)

func debugAppDB(ctx context.Context, opsManager *omv1.MongoDBOpsManager, centralClusterName string, memberCluster multicluster.MemberCluster, deployPods bool) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < opsManager.Spec.AppDB.GetMemberClusterSpecByName(memberCluster.Name).Members; podIdx++ {
		templateData := appDBTemplateData(opsManager, memberCluster, podIdx)
		scriptsHash, err := createAppDBConfigMap(ctx, opsManager, memberCluster, podIdx)
		if err != nil {
			return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createAppDBStatefulSetObject(opsManager.Namespace, scriptsHash, templateData, deployPods)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	return attachCommands, nil
}

func debugOM(ctx context.Context, opsManager *omv1.MongoDBOpsManager, reconcilerHelper operator.OpsManagerReconcilerHelper, centralClusterName string, memberCluster multicluster.MemberCluster, deployPods bool) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < memberCluster.Replicas; podIdx++ {
		templateData := omTemplateData(opsManager, reconcilerHelper, memberCluster, podIdx)
		scriptsHash, err := createOMConfigMap(ctx, opsManager, reconcilerHelper, memberCluster, podIdx)
		if err != nil {
			return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createOMDeploymentObject(opsManager.Namespace, scriptsHash, templateData, deployPods)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	if opsManager.Spec.Backup != nil && opsManager.Spec.Backup.Enabled {
		for podIdx := 0; podIdx < reconcilerHelper.BackupDaemonMembersForMemberCluster(memberCluster); podIdx++ {
			templateData := omBackupDaemonTemplateData(opsManager, reconcilerHelper, memberCluster, podIdx)
			scriptsHash, err := createOMBackupDaemonConfigMap(ctx, opsManager, reconcilerHelper, memberCluster, podIdx)
			if err != nil {
				return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", memberCluster.Name, err)
			}

			sts := createOMDeploymentObject(opsManager.Namespace, scriptsHash, templateData, deployPods)
			if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
				return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", memberCluster.Name, err)
			}
			attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
		}
	}

	return attachCommands, nil
}

func newAttachCommand(templateData TemplateData, centralClusterName string, memberClusterName string) attachCommand {
	debugPodName := fmt.Sprintf("mdb-debug-%s-0", templateData.PodName)
	debugStsName := fmt.Sprintf("mdb-debug-%s", templateData.PodName)
	if memberClusterName == multicluster.LegacyCentralClusterName {
		memberClusterName = centralClusterName
	}
	attachCommand := attachCommand{
		Command:         createKubectlAttachCommand(centralClusterName, memberClusterName, templateData.Namespace, templateData.PodName, debugPodName),
		ShortName:       templateData.ShortName,
		PodName:         templateData.PodName,
		DebugPodName:    debugPodName,
		DebugStsName:    debugStsName,
		ResourceType:    templateData.ResourceType,
		ResourceName:    templateData.ResourceName,
		OperatorContext: centralClusterName,
		DebugPodContext: memberClusterName,
		Namespace:       templateData.Namespace,
	}
	return attachCommand
}

func createStatefulSet(ctx context.Context, sts appsv1.StatefulSet, client kubernetesClient.Client) error {
	stsExists := true
	namespacedName := types.NamespacedName{
		Namespace: sts.Namespace,
		Name:      sts.Name,
	}
	existingSts := appsv1.StatefulSet{}
	err := client.Get(ctx, namespacedName, &existingSts)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			stsExists = false
		} else {
			return xerrors.Errorf("failed to get statefulset: %v: %w", namespacedName, err)
		}
	}

	if stsExists {
		sts.Spec.Replicas = existingSts.Spec.Replicas
		if err := client.Update(ctx, &sts); err != nil {
			return xerrors.Errorf("failed to update statefulset: %v: %w", namespacedName, err)
		}
	} else {
		if err := client.Create(ctx, &sts); err != nil {
			return xerrors.Errorf("failed to create statefulset: %v: %w", namespacedName, err)
		}
	}

	return nil
}

func createAppDBStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool) appsv1.StatefulSet {
	deploymentName := fmt.Sprintf("mdb-debug-%s", templateData.PodName)

	command := `
set -x
cp /scripts/entrypoint.sh ./entrypoint.sh
chmod +x ./entrypoint.sh
cat entrypoint.sh
./entrypoint.sh
`
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
			Labels:    mdbDebugLabels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: replicas(deployPods),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": deploymentName,
				}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": deploymentName,
					},
					Annotations: map[string]string{
						"scripts-hash": scriptsHash,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "mdb-debug-sa-cluster-admin",
					// Affinity rules are not necessary on Kind
					// but in cloud (i.e. GKE) we need to co-locate debug pods with appdb pods
					// on the same node to allow for multiple mounts to the same PV.
					Affinity: &corev1.Affinity{
						PodAffinity: &corev1.PodAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{
								{
									LabelSelector: &metav1.LabelSelector{
										MatchExpressions: []metav1.LabelSelectorRequirement{
											{
												Key:      "statefulset.kubernetes.io/pod-name",
												Operator: metav1.LabelSelectorOpIn,
												Values:   []string{templateData.PodName},
											},
										},
									},
									TopologyKey: "kubernetes.io/hostname",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: templateData.VolumeName,
								},
							},
						},
						{
							Name: "automation-config",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									DefaultMode: pointer.Int32(416),
									SecretName:  fmt.Sprintf("%s-config", templateData.ResourceName),
								},
							},
						},
						{
							Name: "scripts",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: fmt.Sprintf("mdb-debug-scripts-%s", templateData.PodName)},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "mdb-debug",
							Image:           "quay.io/lsierant/diffwatch:latest",
							ImagePullPolicy: corev1.PullAlways,
							TTY:             true,
							Command:         []string{"/bin/bash", "-c", command},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/data",
									Name:      "data",
								},
								{
									MountPath: "/scripts",
									Name:      "scripts",
								},
								{
									MountPath: "/data/ac",
									Name:      "automation-config",
								},
							},
						},
					},
				},
			},
		},
	}
}

func replicas(deployPods bool) *int32 {
	if deployPods {
		return ptr.To(int32(1))
	}
	return ptr.To(int32(0))
}

func createOMDeploymentObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool) appsv1.StatefulSet {
	deploymentName := fmt.Sprintf("mdb-debug-%s", templateData.PodName)

	command := `
set -x
cp /scripts/entrypoint.sh ./entrypoint.sh
chmod +x ./entrypoint.sh
cat entrypoint.sh
./entrypoint.sh
`

	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      deploymentName,
			Namespace: namespace,
			Labels:    mdbDebugLabels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: replicas(deployPods),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": deploymentName,
				}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": deploymentName,
					},
					Annotations: map[string]string{
						"scripts-hash": scriptsHash,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "mdb-debug-sa-cluster-admin",
					Volumes: []corev1.Volume{
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
								//PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								//
								//	ClaimName: fmt.Sprintf("data-%s", templateData.PodName),
								//},
							},
						},
						{
							Name: "scripts",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: fmt.Sprintf("mdb-debug-scripts-%s", templateData.PodName)},
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:            "mdb-debug",
							Image:           "quay.io/lsierant/diffwatch:latest",
							ImagePullPolicy: corev1.PullAlways,
							TTY:             true,
							Command:         []string{"/bin/bash", "-c", command},
							VolumeMounts: []corev1.VolumeMount{
								{
									MountPath: "/data",
									Name:      "data",
								},
								{
									MountPath: "/scripts",
									Name:      "scripts",
								},
							},
						},
					},
				},
			},
		},
	}
}

func createAppDBConfigMap(ctx context.Context, opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster, podIdx int) (string, error) {
	templateData := appDBTemplateData(opsManager, memberCluster, podIdx)
	appDBEntryPoint, err := renderTemplate("appdb_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("appdb_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, opsManager.Namespace, memberCluster.Client, appDBConfigMapName(opsManager.Spec.AppDB, memberCluster, podIdx), appDBEntryPoint, tmuxSession)
}

func createOMConfigMap(ctx context.Context, opsManager *omv1.MongoDBOpsManager, reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) (string, error) {
	templateData := omTemplateData(opsManager, reconcilerHelper, memberCluster, podIdx)
	entryPoint, err := renderTemplate("om_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("om_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, opsManager.Namespace, memberCluster.Client, omConfigMapName(reconcilerHelper, memberCluster, podIdx), entryPoint, tmuxSession)
}

func createOMBackupDaemonConfigMap(ctx context.Context, opsManager *omv1.MongoDBOpsManager, reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) (string, error) {
	templateData := omBackupDaemonTemplateData(opsManager, reconcilerHelper, memberCluster, podIdx)
	entryPoint, err := renderTemplate("om_backup_daemon_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("om_backup_daemon_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render om_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, opsManager.Namespace, memberCluster.Client, omBackupDaemonConfigMapName(reconcilerHelper, memberCluster, podIdx), entryPoint, tmuxSession)
}

func createConfigMap(ctx context.Context, namespace string, client client.Client, configMapName string, entrypointScript string, tmuxSessionScript string) (string, error) {
	hasher := sha1.New()
	_, _ = io.WriteString(hasher, entrypointScript+tmuxSessionScript)
	scriptsHash := base64.StdEncoding.EncodeToString(hasher.Sum(nil))

	configMap := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
			Annotations: map[string]string{
				"scripts-hash": scriptsHash,
			},
			Labels: mdbDebugLabels,
		},
		Data: map[string]string{
			"entrypoint.sh": entrypointScript,
			"session.yaml":  tmuxSessionScript,
		},
	}
	if err := configmap.CreateOrUpdate(ctx, kubernetesClient.NewClient(client), configMap); err != nil {
		return "", xerrors.Errorf("failed to update config map %s: %w", configMap.Name, err)
	}

	return scriptsHash, nil
}

func appDBTemplateData(opsManager *omv1.MongoDBOpsManager, memberCluster multicluster.MemberCluster, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     opsManager.Namespace,
		ResourceName:  opsManager.Spec.AppDB.Name(),
		ResourceType:  "om",
		StsName:       opsManager.Spec.AppDB.NameForCluster(memberCluster.Index),
		PodName:       fmt.Sprintf("%s-%d", opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("appdb-%d-%d", memberCluster.Index, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(opsManager.Annotations),
		ContainerName: "mongodb-agent",
		VolumeName:    fmt.Sprintf("data-%s-%d", opsManager.Spec.AppDB.NameForCluster(memberCluster.Index), podIdx),
		BaseLogDir:    "/data/logs",
	}
}

func appDBConfigMapName(appDB omv1.AppDBSpec, memberCluster multicluster.MemberCluster, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-%s-%d", appDB.NameForCluster(memberCluster.Index), podIdx)
}

func omTemplateData(opsManager *omv1.MongoDBOpsManager, reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     opsManager.Namespace,
		ResourceName:  opsManager.Name,
		ResourceType:  "om",
		StsName:       reconcilerHelper.OpsManagerStatefulSetNameForMemberCluster(memberCluster),
		PodName:       fmt.Sprintf("%s-%d", reconcilerHelper.OpsManagerStatefulSetNameForMemberCluster(memberCluster), podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("om-%d-%d", memberCluster.Index, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(opsManager.Annotations),
		ContainerName: "mongodb-ops-manager",
	}
}

func omBackupDaemonTemplateData(opsManager *omv1.MongoDBOpsManager, reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     opsManager.Namespace,
		ResourceName:  opsManager.Name,
		ResourceType:  "om",
		StsName:       reconcilerHelper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster),
		PodName:       fmt.Sprintf("%s-%d", reconcilerHelper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("om-bd-%d-%d", memberCluster.Index, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(opsManager.Annotations),
		ContainerName: "mongodb-ops-manager",
	}
}

func omConfigMapName(reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-%s-%d", reconcilerHelper.OpsManagerStatefulSetNameForMemberCluster(memberCluster), podIdx)
}

func omBackupDaemonConfigMapName(reconcilerHelper operator.OpsManagerReconcilerHelper, memberCluster multicluster.MemberCluster, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-%s-%d", reconcilerHelper.BackupDaemonStatefulSetNameForMemberCluster(memberCluster), podIdx)
}
