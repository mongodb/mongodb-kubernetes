package construct

import (
	"fmt"
	mdbmultiv1 "github.com/10gen/ops-manager-kubernetes/api/v1/mdbmulti"
	"github.com/10gen/ops-manager-kubernetes/controllers/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	resource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// For testing remove this later
func int32Ptr(i int32) *int32                                              { return &i }
func int64Ptr(i int64) *int64                                              { return &i }
func boolPtr(b bool) *bool                                                 { return &b }
func pvModePtr(s corev1.PersistentVolumeMode) *corev1.PersistentVolumeMode { return &s }

func MultiClusterStatefulSet(mdbm mdbmultiv1.MongoDBMulti, conn om.Connection) appsv1.StatefulSet {
	return appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mdbm.Name,
			Namespace: mdbm.Spec.Namespace,
			Labels: map[string]string{
				"app":     mdbm.Name + "-svc",
				"manager": "mongodb-enterprise-operator",
			},
		},
		Spec: appsv1.StatefulSetSpec{
			Replicas: int32Ptr(1),
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"app":               mdbm.Name + "-svc",
					"controller":        "mongodb-enterprise-operator",
					"pod-anti-affinity": mdbm.Name,
				},
			},
			ServiceName: mdbm.Name + "-svc",
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":               mdbm.Name + "-svc",
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
					// FIXME: Not the actual SA we want to use this has all permissions.
					ServiceAccountName: "mongodb-enterprise-operator-multi-cluster",
					Containers: []corev1.Container{
						{
							Image: "quay.io/mongodb/mongodb-enterprise-database:2.0.0",
							Name:  "mongodb-enterprise-database",
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
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:    int64Ptr(2000),
								RunAsNonRoot: boolPtr(true),
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
									Value: "-logFile,/var/log/mongodb-mms-automation/automation-agent.log,",
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
									Value: conn.User(),
								},
							},
						},
					},
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot: boolPtr(true),
						FSGroup:      int64Ptr(2000),
					},
					Volumes: []corev1.Volume{
						{
							Name: "database-scripts",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
					TerminationGracePeriodSeconds: int64Ptr(600),
					InitContainers: []corev1.Container{
						{
							Name:            "mongodb-enterprise-init-database",
							Image:           "quay.io/mongodb/mongodb-enterprise-init-database:1.0.3",
							ImagePullPolicy: corev1.PullAlways,
							SecurityContext: &corev1.SecurityContext{
								RunAsUser:    int64Ptr(2000),
								RunAsNonRoot: boolPtr(true),
							},
							VolumeMounts: []corev1.VolumeMount{
								{
									Name:      "database-scripts",
									MountPath: "/opt/scripts",
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
}
