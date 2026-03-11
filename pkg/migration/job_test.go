package migration

import (
	"testing"

	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// TestBuildJobFromStatefulSet_IncludesCredentials asserts the Job gets credential volumes from the STS.
func TestBuildJobFromStatefulSet_IncludesCredentials(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{
						Name: util.ClusterFileName,
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "my-rs-clusterfile"},
						},
					}},
					Containers: []corev1.Container{{
						VolumeMounts: []corev1.VolumeMount{{
							Name:      util.ClusterFileName,
							MountPath: "/var/run/credentials",
							ReadOnly:  true,
						}},
					}},
				},
			},
		},
	}
	rs := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "default"}}
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	assert.NotEmpty(t, job.Spec.Template.Spec.Volumes)
	assert.NotEmpty(t, job.Spec.Template.Spec.Containers[0].VolumeMounts)
}

// TestBuildJobFromStatefulSet_ExcludesPVCVolumes asserts that volumes backed by a PVC (e.g. data, logs) are not copied to the Job.
func TestBuildJobFromStatefulSet_ExcludesPVCVolumes(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: util.ClusterFileName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "my-rs-clusterfile"},
							},
						},
						{
							Name: "data",
							VolumeSource: corev1.VolumeSource{
								PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-my-rs-0"},
							},
						},
					},
					Containers: []corev1.Container{{
						VolumeMounts: []corev1.VolumeMount{
							{Name: util.ClusterFileName, MountPath: "/var/run/credentials", ReadOnly: true},
							{Name: "data", MountPath: "/data", ReadOnly: false},
						},
					}},
				},
			},
		},
	}
	rs := &mdbv1.MongoDB{ObjectMeta: metav1.ObjectMeta{Name: "my-rs", Namespace: "default"}}
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	assert.Len(t, job.Spec.Template.Spec.Volumes, 1)
	assert.Equal(t, util.ClusterFileName, job.Spec.Template.Spec.Volumes[0].Name)
	assert.Len(t, job.Spec.Template.Spec.Containers[0].VolumeMounts, 1)
	assert.Equal(t, util.ClusterFileName, job.Spec.Template.Spec.Containers[0].VolumeMounts[0].Name)
}
