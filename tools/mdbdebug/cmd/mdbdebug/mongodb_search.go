package main

import (
	"context"
	"fmt"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/search"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func mongoDBSearchTemplateData(mdbs *search.MongoDBSearch, podIdx int) TemplateData {
	podName := fmt.Sprintf("%s-search-%d", mdbs.Name, podIdx)
	return TemplateData{
		Namespace:     mdbs.Namespace,
		ResourceName:  mdbs.Name,
		ResourceType:  "mdbs",
		StsName:       fmt.Sprintf("%s-search", mdbs.Name),
		PodName:       podName,
		PodIdx:        podIdx,
		ClusterIdx:    0,
		ShortName:     podName,
		StaticArch:    true,
		ContainerName: "mongot",
		VolumeName:    fmt.Sprintf("data-%s-search-%d", mdbs.Name, podIdx),
		BaseLogDir:    "/logs",
	}
}

func mongoDBSearchShardedTemplateData(mdbs *search.MongoDBSearch, shardIdx int, podIdx int) TemplateData {
	podName := fmt.Sprintf("%s-search-%d-%d", mdbs.Name, shardIdx, podIdx)
	return TemplateData{
		Namespace:     mdbs.Namespace,
		ResourceName:  mdbs.Name,
		ResourceType:  "mdbs",
		StsName:       fmt.Sprintf("%s-%d-search", shardIdx, mdbs.Name),
		PodName:       podName,
		PodIdx:        podIdx,
		ClusterIdx:    0,
		ShortName:     podName,
		StaticArch:    true,
		ContainerName: "mongot",
		VolumeName:    fmt.Sprintf("data-%s-search--%d-%d", mdbs.Name, shardIdx, podIdx),
		BaseLogDir:    "/logs",
	}
}

func mongoDBSearchConfigMapName(mdbs *search.MongoDBSearch, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-mdbs-%s-search-%d", mdbs.Name, podIdx)
}

func mongoDBSearchShardedConfigMapName(mdbs *search.MongoDBSearch, shardIdx int, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-mdbs-%s-search-%d-%d", mdbs.Name, shardIdx, podIdx)
}

func createMongoDBSearchConfigMap(ctx context.Context, mdbc *search.MongoDBSearch, client client.Client, podIdx int) (string, error) {
	templateData := mongoDBSearchTemplateData(mdbc, podIdx)
	entryPoint, err := renderTemplate("mongot_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("mongot_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, mdbc.Namespace, client, mongoDBSearchConfigMapName(mdbc, podIdx), entryPoint, tmuxSession)
}

func createMongoDBSearchShardedConfigMap(ctx context.Context, mdbc *search.MongoDBSearch, client client.Client, shardIdx int, podIdx int) (string, error) {
	templateData := mongoDBSearchShardedTemplateData(mdbc, shardIdx, podIdx)
	entryPoint, err := renderTemplate("mongot_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("mongot_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, mdbc.Namespace, client, mongoDBSearchShardedConfigMapName(mdbc, shardIdx, podIdx), entryPoint, tmuxSession)
}

func debugMongoDBSearch(ctx context.Context, namespace string, name string, centralClusterName string, client kubernetesClient.Client, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	mdbc := &search.MongoDBSearch{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, mdbc); err != nil {
		return nil, xerrors.Errorf("error getting resource %s/%s", namespace, name)
	}

	sharded := false
	sourceMongoDBResourceRef := mdbc.GetMongoDBResourceRef()
	if sourceMongoDBResourceRef == nil {
		return nil, xerrors.New("MongoDBSearch source MongoDB resource reference is not set")
	}
	sourceName := types.NamespacedName{Namespace: mdbc.GetNamespace(), Name: sourceMongoDBResourceRef.Name}
	mdb := &mdbv1.MongoDB{}
	if err := client.Get(ctx, sourceName, mdb); err != nil {
		zap.S().Errorf("error getting resource %s/%s", namespace, name)
	} else {
		if mdb.IsShardedCluster() {
			sharded = true
		}
	}

	if sharded {
		return debugMongoDBSearchSharded(ctx, namespace, client, centralClusterName, mdbc, deployPods, diffwatchImage, mdb.Spec.ShardCount)
	}
	return debugMongoDBSearchForRS(ctx, namespace, client, centralClusterName, mdbc, deployPods, diffwatchImage)
}

func debugMongoDBSearchSharded(ctx context.Context, namespace string, client kubernetesClient.Client, centralClusterName string, mdbc *search.MongoDBSearch, deployPods bool, diffwatchImage string, shardCount int) ([]attachCommand, error) {
	if err := createServiceAccountAndRoles(ctx, client, namespace); err != nil {
		return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", centralClusterName, err)
	}

	var attachCommands []attachCommand
	for shardIdx := 0; shardIdx < shardCount; shardIdx++ {
		for podIdx := 0; podIdx < 1; podIdx++ {
			templateData := mongoDBSearchShardedTemplateData(mdbc, shardIdx, podIdx)
			scriptsHash, err := createMongoDBSearchShardedConfigMap(ctx, mdbc, client, shardIdx, podIdx)
			if err != nil {
				return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", centralClusterName, err)
			}

			sts := createSearchStatefulSetObject(mdbc.Namespace, scriptsHash, templateData, deployPods, diffwatchImage)
			if err = createStatefulSet(ctx, sts, client); err != nil {
				return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", centralClusterName, err)
			}

			attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, centralClusterName))
		}
	}

	return attachCommands, nil
}

func debugMongoDBSearchForRS(ctx context.Context, namespace string, client kubernetesClient.Client, centralClusterName string, mdbc *search.MongoDBSearch, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	if err := createServiceAccountAndRoles(ctx, client, namespace); err != nil {
		return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", centralClusterName, err)
	}

	var attachCommands []attachCommand
	for podIdx := 0; podIdx < 1; podIdx++ {
		templateData := mongoDBSearchTemplateData(mdbc, podIdx)
		scriptsHash, err := createMongoDBSearchConfigMap(ctx, mdbc, client, podIdx)
		if err != nil {
			return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", centralClusterName, err)
		}

		sts := createSearchStatefulSetObject(mdbc.Namespace, scriptsHash, templateData, deployPods, diffwatchImage)
		if err = createStatefulSet(ctx, sts, client); err != nil {
			return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", centralClusterName, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, centralClusterName))
	}

	return attachCommands, nil
}

func createSearchStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool, diffwatchImage string) appsv1.StatefulSet {
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
				},
			},
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
							Name: "scripts",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: fmt.Sprintf("mdb-debug-scripts-%s-%s", templateData.ResourceType, templateData.PodName)},
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
									MountPath: "/logs",
									SubPath:   "mdb-debug",
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
