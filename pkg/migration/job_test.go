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

func TestVolumesAndMountsFromStatefulSet_DeduplicatesSameMountAcrossContainers(t *testing.T) {
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
					Containers: []corev1.Container{
						{VolumeMounts: []corev1.VolumeMount{{
							Name:      util.ClusterFileName,
							MountPath: "/var/run/credentials",
							ReadOnly:  true,
						}}},
						{VolumeMounts: []corev1.VolumeMount{{
							Name:      util.ClusterFileName,
							MountPath: "/var/run/credentials",
							ReadOnly:  true,
						}}},
					},
				},
			},
		},
	}
	_, mounts := volumesAndMountsFromStatefulSet(sts)
	assert.Len(t, mounts, 1)
	assert.Equal(t, util.ClusterFileName, mounts[0].Name)
}

func TestVolumesAndMountsFromStatefulSet_UnionsDistinctMountsFromContainers(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{
							Name: util.ClusterFileName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "cluster"},
							},
						},
						{
							Name: util.AgentSecretName,
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{SecretName: "agent"},
							},
						},
					},
					Containers: []corev1.Container{
						{VolumeMounts: []corev1.VolumeMount{{
							Name: util.ClusterFileName, MountPath: "/cluster", ReadOnly: true,
						}}},
						{VolumeMounts: []corev1.VolumeMount{{
							Name: util.AgentSecretName, MountPath: "/agent", ReadOnly: true,
						}}},
					},
				},
			},
		},
	}
	_, mounts := volumesAndMountsFromStatefulSet(sts)
	assert.Len(t, mounts, 2)
}

func TestBuildJobFromStatefulSet_AuthMechanism_SCRAMSHA1(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.SCRAMSHA1}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	var authMechanism string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "AUTH_MECHANISM" {
			authMechanism = e.Value
			break
		}
	}
	assert.Equal(t, util.SCRAMSHA1, authMechanism)
}

func TestBuildJobFromStatefulSet_AuthMechanism_SCRAMUmbrellaMongoDBCR(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.SCRAMSHA256, util.X509}).
		EnableAgentAuth(util.SCRAM).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha1Option, "", "")

	var authMechanism string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "AUTH_MECHANISM" {
			authMechanism = e.Value
			break
		}
	}
	// SCRAM umbrella + OM autoAuthMechanism MONGODB-CR resolves to mechanism name MONGODB-CR (see authentication.MechanismName).
	assert.Equal(t, util.AutomationConfigScramSha1Option, authMechanism)
}

func TestBuildJobFromStatefulSet_ClientCertRequired_True(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.X509}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	// Simulate a clientCertificateSecretRef being set, which triggers ShouldUseClientCertificates().
	rs.GetSecurity().Authentication.Agents.ClientCertificateSecretRefWrap.ClientCertificateSecretRef.Name = "agent-cert"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, "MONGODB-X509", "", "")

	var clientCertRequired string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CLIENT_CERT_REQUIRED" {
			clientCertRequired = e.Value
			break
		}
	}
	assert.Equal(t, "true", clientCertRequired)
}

func TestBuildJobFromStatefulSet_ClientCertRequired_False(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.SCRAMSHA256}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	// No ClientCertificateSecretRef set — ShouldUseClientCertificates() returns false.
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	var clientCertRequired string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "CLIENT_CERT_REQUIRED" {
			clientCertRequired = e.Value
			break
		}
	}
	assert.Equal(t, "false", clientCertRequired)
}

// TestBuildJobFromStatefulSet_ShardedCluster verifies that BuildJobFromStatefulSet works correctly
// when called with a ShardedCluster MongoDB resource, using a config server StatefulSet as input.
func TestBuildJobFromStatefulSet_ShardedCluster(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{{
						Name: util.ClusterFileName,
						VolumeSource: corev1.VolumeSource{
							Secret: &corev1.SecretVolumeSource{SecretName: "my-sc-clusterfile"},
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

	mdb := &mdbv1.MongoDB{
		ObjectMeta: metav1.ObjectMeta{Name: "my-sc", Namespace: "default"},
	}
	mdb.Spec.ResourceType = mdbv1.ShardedCluster

	wantConnStr := "mongodb://cfg-host:27017/?replicaSet=my-sc-config"
	wantExtMembers := []string{"10.0.0.1:27017", "10.0.0.2:27017"}

	job := BuildJobFromStatefulSet(mdb, sts, "img", wantConnStr, wantExtMembers, util.AutomationConfigScramSha256Option, "", "")

	assert.Equal(t, "my-sc-connectivity-check", job.Name)

	envByName := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envByName[e.Name] = e.Value
	}
	assert.Equal(t, wantConnStr, envByName["CONNECTION_STRING"])
	assert.Equal(t, "10.0.0.1:27017 10.0.0.2:27017", envByName["EXTERNAL_MEMBERS"])

	assert.Equal(t, "my-sc", job.Labels[ConnectivityCheckReplicaSetLabel])
	assert.Equal(t, "true", job.Labels[ConnectivityCheckDryRunLabel])
	assert.Equal(t, OperatorManagedByValue, job.Labels[OperatorManagedByLabel])
}

func TestBuildJobFromStatefulSet_CustomCAFilePath(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.X509}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	rs.Spec.Security.TLSConfig = &mdbv1.TLSConfig{
		Enabled:    true,
		CAFilePath: "/etc/ssl/certs/ca.pem",
	}
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, "MONGODB-X509", "hashkey", "")

	envMap := map[string]string{}
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	assert.Equal(t, "/etc/ssl/certs/ca.pem", envMap["MONGOD_TLS_CA_PATH"], "MONGOD_TLS_CA_PATH should use spec.security.tls.caFilePath when set")
}

func TestBuildJobFromStatefulSet_SubjectDN(t *testing.T) {
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
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.X509}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	wantDN := "CN=mms-automation-agent,O=MongoDB"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, "MONGODB-X509", "hashkey", wantDN)

	var subjectDN string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "SUBJECT_DN" {
			subjectDN = e.Value
			break
		}
	}
	assert.Equal(t, wantDN, subjectDN)
}

func TestBuildJobFromStatefulSet_MongodTLSCAPath_SetWhenTLSEnabled(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes:    []corev1.Volume{},
					Containers: []corev1.Container{{}},
				},
			},
		},
	}
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.SCRAMSHA256}).
		SetSecurityTLSEnabled().
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	envMap := make(map[string]string)
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		envMap[e.Name] = e.Value
	}
	assert.NotEmpty(t, envMap["MONGOD_TLS_CA_PATH"], "MONGOD_TLS_CA_PATH must be set when mongod TLS is enabled")
	assert.Equal(t, util.TLSCaMountPath+"/ca-pem", envMap["MONGOD_TLS_CA_PATH"])
}

func TestBuildJobFromStatefulSet_MongodTLSCAPath_EmptyWhenTLSDisabled(t *testing.T) {
	sts := &appsv1.StatefulSet{
		Spec: appsv1.StatefulSetSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Volumes:    []corev1.Volume{},
					Containers: []corev1.Container{{}},
				},
			},
		},
	}
	rs := mdbv1.NewReplicaSetBuilder().
		EnableAuth([]mdbv1.AuthMode{util.SCRAMSHA256}).
		Build()
	rs.Name = "my-rs"
	rs.Namespace = "default"
	job := BuildJobFromStatefulSet(rs, sts, "img", "mongodb://host:27017/?replicaSet=my-rs", nil, util.AutomationConfigScramSha256Option, "", "")

	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == "MONGOD_TLS_CA_PATH" {
			assert.Empty(t, e.Value, "MONGOD_TLS_CA_PATH must be empty when TLS is disabled")
		}
	}
}
