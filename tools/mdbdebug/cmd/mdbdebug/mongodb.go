package main

import (
	"context"
	"fmt"
	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator"
	"github.com/mongodb/mongodb-kubernetes/pkg/dns"
	"github.com/mongodb/mongodb-kubernetes/pkg/multicluster"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
	"golang.org/x/xerrors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func debugMongos(ctx context.Context, mdb *mdbv1.MongoDB, centralClusterName string, reconcilerHelper *operator.ShardedClusterReconcileHelper, memberCluster multicluster.MemberCluster, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < reconcilerHelper.GetMongosScaler(memberCluster).DesiredReplicas(); podIdx++ {
		stsName := reconcilerHelper.GetMongosStsName(memberCluster)
		templateData := mongosTemplateData(mdb, memberCluster, stsName, podIdx)
		scriptsHash, err := renderTemplatesAndCreateConfigMap(ctx, memberCluster, templateData, podConfigMapName(podName(stsName, podIdx)), "mongos_entrypoint.sh.tpl", "mongos_tmux_session.yaml.tpl")
		if err != nil {
			return nil, xerrors.Errorf("error creating mongos config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createMongosStatefulSetObject(mdb.Namespace, scriptsHash, templateData, deployPods, diffwatchImage)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating mongos statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	return attachCommands, nil
}

func debugConfigServers(ctx context.Context, mdb *mdbv1.MongoDB, centralClusterName string, reconcilerHelper *operator.ShardedClusterReconcileHelper, memberCluster multicluster.MemberCluster, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < reconcilerHelper.GetConfigSrvScaler(memberCluster).DesiredReplicas(); podIdx++ {
		stsName := reconcilerHelper.GetConfigSrvStsName(memberCluster)
		templateData := configServerTemplateData(mdb, memberCluster, stsName, podIdx)
		scriptsHash, err := renderTemplatesAndCreateConfigMap(ctx, memberCluster, templateData, podConfigMapName(podName(stsName, podIdx)), "replicaset_entrypoint.sh.tpl", "replicaset_tmux_session.yaml.tpl")
		if err != nil {
			return nil, xerrors.Errorf("error creating mongos config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createReplicaSetStatefulSetObject(mdb.Namespace, scriptsHash, templateData, deployPods, diffwatchImage)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating config server statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	return attachCommands, nil
}

func debugShardsServers(ctx context.Context, mdb *mdbv1.MongoDB, centralClusterName string, reconcilerHelper *operator.ShardedClusterReconcileHelper, shardIdx int, memberCluster multicluster.MemberCluster, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < reconcilerHelper.GetShardScaler(shardIdx, memberCluster).DesiredReplicas(); podIdx++ {
		stsName := reconcilerHelper.GetShardStsName(shardIdx, memberCluster)
		templateData := shardTemplateData(mdb, memberCluster, stsName, shardIdx, podIdx)
		scriptsHash, err := renderTemplatesAndCreateConfigMap(ctx, memberCluster, templateData, podConfigMapName(podName(stsName, podIdx)), "replicaset_entrypoint.sh.tpl", "replicaset_tmux_session.yaml.tpl")
		if err != nil {
			return nil, xerrors.Errorf("error creating mongos config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createReplicaSetStatefulSetObject(mdb.Namespace, scriptsHash, templateData, deployPods, diffwatchImage)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating config server statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	return attachCommands, nil
}

func debugReplicaSetPods(ctx context.Context, resourceNamespace string, resourceName string, mdb *mdbv1.DbCommonSpec, mdbAnnotations map[string]string, centralClusterName string, memberCluster multicluster.MemberCluster, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	var attachCommands []attachCommand
	for podIdx := 0; podIdx < memberCluster.Replicas; podIdx++ {
		templateData := replicaSetTemplateData(resourceNamespace, resourceName, mdb, mdbAnnotations, memberCluster, podIdx)
		stsName := replicaSetStatefulSetName(resourceName, memberCluster)
		scriptsHash, err := renderTemplatesAndCreateConfigMap(ctx, memberCluster, templateData, podConfigMapName(podName(stsName, podIdx)), "replicaset_entrypoint.sh.tpl", "replicaset_tmux_session.yaml.tpl")
		if err != nil {
			return nil, xerrors.Errorf("error creating mongos config map in cluster %s: %w", memberCluster.Name, err)
		}

		sts := createReplicaSetStatefulSetObject(resourceNamespace, scriptsHash, templateData, deployPods, diffwatchImage)
		if err = createStatefulSet(ctx, sts, memberCluster.Client); err != nil {
			return nil, xerrors.Errorf("error creating config server statefulset in cluster %s: %w", memberCluster.Name, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, memberCluster.Name))
	}

	return attachCommands, nil
}

var mdbDebugLabels = map[string]string{
	"mdb-debug": "true",
}

func createReplicaSetStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool, diffwatchImage string) appsv1.StatefulSet {
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
									ClaimName: fmt.Sprintf("data-%s", templateData.PodName),
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
							Image:           diffwatchImage,
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

func createMongosStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool, diffwatchImage string) appsv1.StatefulSet {
	stsName := fmt.Sprintf("mdb-debug-%s", templateData.PodName)

	command := `
set -x
cp /scripts/entrypoint.sh ./entrypoint.sh
chmod +x ./entrypoint.sh
cat entrypoint.sh
./entrypoint.sh
`
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      stsName,
			Namespace: namespace,
			Labels:    mdbDebugLabels,
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: replicas(deployPods),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app": stsName,
				}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app": stsName,
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
							Image:           diffwatchImage,
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

func renderTemplatesAndCreateConfigMap(ctx context.Context, memberCluster multicluster.MemberCluster, templateData TemplateData, configMapName string, entrypointTemplateName string, tmuxSessionTemplateName string) (string, error) {
	entrypoint, err := renderTemplate(entrypointTemplateName, templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render %s: %w", entrypointTemplateName, err)
	}

	tmuxSession, err := renderTemplate(tmuxSessionTemplateName, templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render %s: %w", tmuxSessionTemplateName, err)
	}

	return createConfigMap(ctx, templateData.Namespace, memberCluster.Client, configMapName, entrypoint, tmuxSession)
}

func mongosTemplateData(mdb *mdbv1.MongoDB, memberCluster multicluster.MemberCluster, stsName string, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     mdb.Namespace,
		ResourceName:  mdb.Name,
		ResourceType:  "mdb",
		StsName:       stsName,
		PodName:       fmt.Sprintf("%s-%d", stsName, podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("mongos-%d-%d", memberCluster.Index, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(mdb.Annotations),
		PodFQDN:       getPodFQDN(mdb.Namespace, mdb.Name+"-mongos", &mdb.Spec.DbCommonSpec, memberCluster, podIdx),
		ContainerName: containerName(architectures.IsRunningStaticArchitecture(mdb.Annotations)),
	}
}

func configServerTemplateData(mdb *mdbv1.MongoDB, memberCluster multicluster.MemberCluster, stsName string, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     mdb.Namespace,
		ResourceName:  mdb.Name,
		ResourceType:  "mdb",
		StsName:       stsName,
		PodName:       fmt.Sprintf("%s-%d", stsName, podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("cs-%d-%d", memberCluster.Index, podIdx),
		PodFQDN:       getPodFQDN(mdb.Namespace, mdb.Name+"-config", &mdb.Spec.DbCommonSpec, memberCluster, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(mdb.Annotations),
		ContainerName: containerName(architectures.IsRunningStaticArchitecture(mdb.Annotations)),
	}
}

func replicaSetTemplateData(resourceNamespace string, resourceName string, mdb *mdbv1.DbCommonSpec, mdbAnnotations map[string]string, memberCluster multicluster.MemberCluster, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     resourceNamespace,
		ResourceName:  resourceName,
		ResourceType:  "mdb",
		StsName:       replicaSetStatefulSetName(resourceName, memberCluster),
		PodName:       fmt.Sprintf("%s-%d", replicaSetStatefulSetName(resourceName, memberCluster), podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		TLSEnabled:    mdb.IsSecurityTLSConfigEnabled(),
		ShortName:     fmt.Sprintf("rs-%d-%d", memberCluster.Index, podIdx),
		PodFQDN:       getPodFQDN(resourceNamespace, resourceName, mdb, memberCluster, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(mdbAnnotations),
		ContainerName: containerName(architectures.IsRunningStaticArchitecture(mdbAnnotations)),
	}
}

func containerName(staticArch bool) string {
	if staticArch {
		return "mongodb-agent"
	} else {
		return "mongodb-enterprise-database"
	}
}

func getPodFQDN(resourceNamespace string, resourceName string, mdb *mdbv1.DbCommonSpec, memberCluster multicluster.MemberCluster, podIdx int) string {
	if memberCluster.Legacy {
		hostnames, _ := dns.GetDNSNames(resourceName, dns.GetServiceName(resourceName), resourceNamespace, "cluster.local", podIdx+1, mdb.GetExternalDomain())
		return hostnames[podIdx]
	} else {
		return dns.GetMultiClusterPodServiceFQDN(resourceName, resourceNamespace, memberCluster.Index, mdb.GetExternalDomain(), podIdx, "cluster.local")
	}
}

func shardTemplateData(mdb *mdbv1.MongoDB, memberCluster multicluster.MemberCluster, stsName string, shardIdx int, podIdx int) TemplateData {
	return TemplateData{
		Namespace:     mdb.Namespace,
		ResourceName:  mdb.Name,
		ResourceType:  "mdb",
		StsName:       stsName,
		PodName:       fmt.Sprintf("%s-%d", stsName, podIdx),
		PodIdx:        podIdx,
		ClusterIdx:    memberCluster.Index,
		ShortName:     fmt.Sprintf("sh-%d-%d-%d", memberCluster.Index, shardIdx, podIdx),
		PodFQDN:       getPodFQDN(mdb.Namespace, fmt.Sprintf("%s-%d", mdb.Name, shardIdx), &mdb.Spec.DbCommonSpec, memberCluster, podIdx),
		StaticArch:    architectures.IsRunningStaticArchitecture(mdb.Annotations),
		ContainerName: containerName(architectures.IsRunningStaticArchitecture(mdb.Annotations)),
	}
}

func replicaSetStatefulSetName(resourceName string, memberCluster multicluster.MemberCluster) string {
	if memberCluster.Legacy {
		return resourceName
	}

	return fmt.Sprintf("%s-%d", resourceName, memberCluster.Index)
}

func podName(statefulSetName string, podIdx int) string {
	return fmt.Sprintf("%s-%d", statefulSetName, podIdx)
}
func podConfigMapName(podName string) string {
	return fmt.Sprintf("mdb-debug-scripts-%s", podName)
}
