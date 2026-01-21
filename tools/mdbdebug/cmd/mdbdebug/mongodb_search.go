package main

import (
	"context"
	"fmt"
	"hash/crc32"

	"go.uber.org/zap"
	"golang.org/x/xerrors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/search"
	"github.com/mongodb/mongodb-kubernetes/controllers/searchcontroller"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
)

func mongoDBSearchConfigMapName(podName string) string {
	return fmt.Sprintf("mdb-debug-scripts-mdbs-%s", podName)
}

func createMongoDBSearchConfigMap(ctx context.Context, namespace string, c kubernetesClient.Client, podName string, templateData TemplateData) (string, error) {
	entryPoint, err := renderTemplate("mongot_entrypoint.sh.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_entrypoint.sh.tpl: %w", err)
	}

	tmuxSession, err := renderTemplate("mongot_tmux_session.yaml.tpl", templateData)
	if err != nil {
		return "", xerrors.Errorf("failed to render mongot_tmux_session.yaml.tpl: %w", err)
	}

	return createConfigMap(ctx, namespace, c, mongoDBSearchConfigMapName(podName), entryPoint, tmuxSession)
}

// debugStsName builds the StatefulSet name for a debug pod.
// The name must be ≤52 chars so that the controller-revision-hash label
// (which appends "-{10-char-hash}" to the STS name) stays within Kubernetes'
// 63-char label-value limit.
// When the naive "mdb-debug-{podName}" is short enough it is used as-is;
// otherwise the pod name is truncated and a stable 8-hex-char CRC-32 checksum of
// the full name is appended to keep it unique.
func debugStsName(podName string) string {
	const maxLen = 52
	full := fmt.Sprintf("mdb-debug-%s", podName)
	if len(full) <= maxLen {
		return full
	}
	// prefix: "mdb-debug-" (10) + up to 33 chars of podName + "-" + 8 hex chars = 52
	prefix := full[:maxLen-9] // leave room for "-" + 8 hex chars
	return fmt.Sprintf("%s-%08x", prefix, crc32.ChecksumIEEE([]byte(full)))
}

// isOwnedByUID reports whether any ownerReference in ownerRefs matches uid.
func isOwnedByUID(ownerRefs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range ownerRefs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

// debugMongoDBSearch creates debug StatefulSets for every mongot pod that belongs to the
// MongoDBSearch resource. It discovers the pods by listing the StatefulSets that are owned
// by the MongoDBSearch (via ownerReference UID), so it works for both replica-set and
// sharded-cluster deployments without any hard-coded naming assumptions.
func debugMongoDBSearch(ctx context.Context, namespace string, name string, centralClusterName string, c kubernetesClient.Client, deployPods bool, diffwatchImage string) ([]attachCommand, error) {
	mdbs := &search.MongoDBSearch{}
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, mdbs); err != nil {
		return nil, xerrors.Errorf("error getting MongoDBSearch resource %s/%s: %w", namespace, name, err)
	}

	if err := createServiceAccountAndRoles(ctx, c, namespace); err != nil {
		return nil, xerrors.Errorf("failed to create service account and roles: %w", err)
	}

	stsList := &appsv1.StatefulSetList{}
	if err := c.List(ctx, stsList, client.InNamespace(namespace)); err != nil {
		return nil, xerrors.Errorf("error listing StatefulSets in namespace %s: %w", namespace, err)
	}

	var attachCommands []attachCommand
	for _, sts := range stsList.Items {
		if !isOwnedByUID(sts.OwnerReferences, mdbs.UID) {
			continue
		}

		// PVC template name — the search controller always uses "data", but read it
		// from the STS so we stay correct if that ever changes.
		pvcTemplateName := "data"
		if len(sts.Spec.VolumeClaimTemplates) > 0 {
			pvcTemplateName = sts.Spec.VolumeClaimTemplates[0].Name
		}

		replicas := int32(1)
		if sts.Spec.Replicas != nil {
			replicas = *sts.Spec.Replicas
		}

		for podIdx := int32(0); podIdx < replicas; podIdx++ {
			// Standard K8s StatefulSet pod/PVC naming:
			//   pod name : {sts-name}-{ordinal}
			//   PVC name : {pvc-template-name}-{sts-name}-{ordinal}
			podName := fmt.Sprintf("%s-%d", sts.Name, podIdx)
			pvcName := fmt.Sprintf("%s-%s-%d", pvcTemplateName, sts.Name, podIdx)

			templateData := TemplateData{
				Namespace:     namespace,
				ResourceName:  mdbs.Name,
				ResourceType:  "mdbs",
				StsName:       sts.Name,
				PodName:       podName,
				PodIdx:        int(podIdx),
				ClusterIdx:    0,
				ShortName:     podName,
				StaticArch:    true,
				ContainerName: searchcontroller.MongotContainerName,
				VolumeName:    pvcName,
				BaseLogDir:    "/logs",
			}

			scriptsHash, err := createMongoDBSearchConfigMap(ctx, namespace, c, podName, templateData)
			if err != nil {
				return nil, xerrors.Errorf("error creating search config map for pod %s: %w", podName, err)
			}

			debugSts := createSearchStatefulSetObject(namespace, scriptsHash, templateData, deployPods, diffwatchImage)
			if err = createStatefulSet(ctx, debugSts, c); err != nil {
				return nil, xerrors.Errorf("error creating debug statefulset for pod %s: %w", podName, err)
			}

			attachCommands = append(attachCommands, newAttachCommand(templateData, centralClusterName, centralClusterName))
		}
	}

	if len(attachCommands) == 0 {
		zap.S().Warnf("No StatefulSets owned by MongoDBSearch %s/%s found — no debug pods deployed", namespace, name)
	}

	return attachCommands, nil
}

func createSearchStatefulSetObject(namespace string, scriptsHash string, templateData TemplateData, deployPods bool, diffwatchImage string) appsv1.StatefulSet {
	deploymentName := debugStsName(templateData.PodName)

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
