package main

import (
	"context"
	"fmt"
	mdbcv1 "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/api/v1"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"golang.org/x/xerrors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func mongoDBCommunityTemplateData(mdbc *mdbcv1.MongoDBCommunity, podIdx int) TemplateData {
	return TemplateData{
		Namespace:        mdbc.Namespace,
		ResourceName:     mdbc.Name,
		ResourceType:     "mdbc",
		StsName:          mdbc.Name,
		PodName:          fmt.Sprintf("%s-%d", mdbc.Name, podIdx),
		PodIdx:           podIdx,
		ClusterIdx:       0,
		ShortName:        fmt.Sprintf("%s-%d", mdbc.Name, podIdx),
		StaticArch:       true,
		ContainerName:    "mongodb-agent",
		MongoDBCommunity: true,
		VolumeName:       fmt.Sprintf("data-volume-%s-%d", mdbc.Name, podIdx),
		BaseLogDir:       "/logs",
	}
}

func mongoDBCommunityConfigMapName(mdbc *mdbcv1.MongoDBCommunity, podIdx int) string {
	return fmt.Sprintf("mdb-debug-scripts-%s-%d", mdbc.Name, podIdx)
}

func createMongoDBCommunityConfigMap(ctx context.Context, mdbc *mdbcv1.MongoDBCommunity, client client.Client, podIdx int) (string, error) {
	templateData := mongoDBCommunityTemplateData(mdbc, podIdx)
	entryPoint, err := renderTemplate("appdb_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render appdb_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("appdb_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render appdb_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, mdbc.Namespace, client, mongoDBCommunityConfigMapName(mdbc, podIdx), entryPoint, tmuxSession)
}

func debugMongoDBCommunity(ctx context.Context, namespace string, name string, centralClusterName string, client kubernetesClient.Client, deployPods bool) ([]attachCommand, error) {
	mdbc := &mdbcv1.MongoDBCommunity{}
	if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, mdbc); err != nil {
		return nil, xerrors.Errorf("error getting resource %s/%s", namespace, name)
	}

	if err := createServiceAccountAndRoles(ctx, client, namespace); err != nil {
		return nil, xerrors.Errorf("failed to create service account and roles in cluster %s: %w", centralClusterName, err)
	}

	var attachCommands []attachCommand
	for podIdx := 0; podIdx < mdbc.Spec.Members; podIdx++ {
		templateData := mongoDBCommunityTemplateData(mdbc, podIdx)
		scriptsHash, err := createMongoDBCommunityConfigMap(ctx, mdbc, client, podIdx)
		if err != nil {
			return nil, xerrors.Errorf("error creating appdb config map in cluster %s: %w", centralClusterName, err)
		}

		sts := createMCOStatefulSetObject(mdbc.Namespace, scriptsHash, templateData, deployPods)
		if err = createStatefulSet(ctx, sts, client); err != nil {
			return nil, xerrors.Errorf("error creating statefulset in cluster %s: %w", centralClusterName, err)
		}

		attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, centralClusterName))
	}

	return attachCommands, nil
}

func createMCOStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool) appsv1.StatefulSet {
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
							Name: "logs",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
									ClaimName: fmt.Sprintf("logs-volume-%s", templateData.PodName),
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
									MountPath: "/logs",
									Name:      "logs",
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
