package construct

import (
	"fmt"

	"github.com/10gen/ops-manager-kubernetes/pkg/tls"

	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/handler"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes-operator/pkg/util/merge"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// For testing remove this later
func int32Ptr(i int32) *int32                                              { return &i }
func int64Ptr(i int64) *int64                                              { return &i }
func pvModePtr(s corev1.PersistentVolumeMode) *corev1.PersistentVolumeMode { return &s }

func MultiClusterStatefulSet(mdbm mdbmultiv1.MongoDBMulti, clusterNum int, memberCount int, conn om.Connection) appsv1.StatefulSet {
	sts := appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      fmt.Sprintf("%s-%d", mdbm.Name, clusterNum),
			Namespace: mdbm.Namespace,
			Labels: map[string]string{
				"controller":   "mongodb-enterprise-operator",
				"mongodbmulti": fmt.Sprintf("%s-%s", mdbm.Namespace, mdbm.Name),
			},
			Annotations: map[string]string{
				handler.MongoDBMultiResourceAnnotation: mdbm.Name,
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(int32(memberCount)),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"controller":        "mongodb-enterprise-operator",
					"pod-anti-affinity": mdbm.Name,
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"controller":        "mongodb-enterprise-operator",
						"pod-anti-affinity": mdbm.Name,
					},
				},
				Spec: corev1.PodSpec{
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							PreferredDuringSchedulingIgnoredDuringExecution: []corev1.WeightedPodAffinityTerm{
								{
									Weight: 100,
									PodAffinityTerm: corev1.PodAffinityTerm{
										TopologyKey: "kubernetes.io/hostname",
										LabelSelector: &metav1.LabelSelector{
											MatchLabels: map[string]string{
												"pod-anti-affinity": mdbm.Name,
											},
										},
									},
								},
							},
						},
					},
					ServiceAccountName: "mongodb-enterprise-database-pods",
					Containers: []corev1.Container{
						{
							Image:           "quay.io/mongodb/mongodb-enterprise-database:2.0.0",
							Name:            "mongodb-enterprise-database",
							SecurityContext: defaultSecurityContext(),
							Ports: []corev1.ContainerPort{
								{
									ContainerPort: 27017,
									Protocol:      "TCP",
								},
							},
							LivenessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{"/opt/scripts/probe.sh"},
									},
								},
								InitialDelaySeconds: 60,
								TimeoutSeconds:      30,
								PeriodSeconds:       30,
								SuccessThreshold:    1,
								FailureThreshold:    6,
							},
							ReadinessProbe: &corev1.Probe{
								Handler: corev1.Handler{
									Exec: &corev1.ExecAction{
										Command: []string{"/opt/scripts/readinessprobe"},
									},
								},
								InitialDelaySeconds: 5,
								TimeoutSeconds:      5,
								PeriodSeconds:       5,
								SuccessThreshold:    1,
								FailureThreshold:    4,
							},
							ImagePullPolicy: corev1.PullAlways,
							Command:         []string{"/opt/scripts/agent-launcher.sh"},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "data",
									MountPath: "/data",
									SubPath:   "data",
								},
								{
									Name:      "data",
									MountPath: "/journal",
									SubPath:   "journal",
								},
								{
									Name:      "data",
									MountPath: "/var/log/mongodb-mms-automation",
									SubPath:   "logs",
								},
								{
									Name:      "database-scripts",
									MountPath: "/opt/scripts",
									ReadOnly:  true,
								},
								{
									Name:      "hostname-override",
									MountPath: "/opt/scripts/config",
								},
							},
							Env: []corev1.EnvVar{
								{
									Name: "AGENT_API_KEY",
									ValueFrom: &corev1.EnvVarSource{
										SecretKeyRef: &corev1.SecretKeySelector{
											LocalObjectReference: corev1.LocalObjectReference{
												Name: fmt.Sprintf("%s-group-secret", conn.GroupID()),
											},
											Key: util.OmAgentApiKey,
										},
									},
								},
								{
									Name:  "AGENT_FLAGS",
									Value: fmt.Sprintf("-logFile,/var/log/mongodb-mms-automation/automation-agent.log,-logLevel,DEBUG,"),
								},
								{
									Name:  "BASE_URL",
									Value: conn.BaseURL(),
								},
								{
									Name:  "GROUP_ID",
									Value: conn.GroupID(),
								},
								{
									Name:  "USER_LOGIN",
									Value: conn.PublicKey(),
								},
								{
									Name:  "MULTI_CLUSTER_MODE",
									Value: "true",
								},
							},
						},
					},
					Volumes: []corev1.Volume{
						{
							Name: "database-scripts",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "hostname-override",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: "hostname-override"},
								},
							},
						},
					},
					TerminationGracePeriodSeconds: int64Ptr(600),
					InitContainers: []corev1.Container{
						{
							Name:            "mongodb-enterprise-init-database",
							Image:           "268558157000.dkr.ecr.eu-west-1.amazonaws.com/raj/ubuntu/mongodb-enterprise-init-database:latest",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: defaultSecurityContext(),
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "database-scripts",
									MountPath: "/opt/scripts",
								},
								{
									Name:      "hostname-override",
									MountPath: "/opt/scripts/config",
								},
							},
						},
					},
				},
			},
			VolumeClaimTemplates: []corev1.PersistentVolumeClaim{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "data",
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								"storage": resource.MustParse("16G"),
							},
						},
						VolumeMode: pvModePtr(corev1.PersistentVolumeFilesystem),
					},
				},
			},
		},
	}

	if mdbm.Spec.GetSecurity().TLSConfig.IsEnabled() {
		tlsConfig := mdbm.Spec.GetSecurity().TLSConfig
		if tlsConfig != nil {
			tls.ConfigureStatefulSet(&sts, mdbm.Name, tlsConfig.SecretRef.Prefix, tlsConfig.CA)
		}
	}

	stsOverride := mdbm.Spec.ClusterSpecList.ClusterSpecs[clusterNum].StatefulSetConfiguration.SpecWrapper.Spec
	stsSpecFinal := merge.StatefulSetSpecs(sts.Spec, stsOverride)
	sts.Spec = stsSpecFinal
	return sts
}
