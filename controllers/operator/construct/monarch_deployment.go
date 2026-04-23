package construct

import (
	"fmt"
	"os"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

const (
	monarchHealthPort      int32 = 8080
	monarchReplicationPort int32 = 9995
	monarchAPIPort         int32 = 1122

	defaultMonarchImage = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-monarch-injector:latest"

	// DefaultMonarchReplicas is the default number of Monarch instances (shippers or injectors)
	// to create per replica set. Multiple instances provide redundancy - the CAS protocol
	// on S3 ensures only one write per oplog entry is accepted, so duplicates are safe.
	DefaultMonarchReplicas = 3
)

// MonarchDeploymentName returns the name for a Monarch Deployment at the given index.
func MonarchDeploymentName(mdbName string, index int) string {
	return fmt.Sprintf("%s-monarch-%d", mdbName, index)
}

// MonarchServiceName returns the name for a Monarch Service at the given index.
func MonarchServiceName(mdbName string, index int) string {
	return fmt.Sprintf("%s-monarch-%d-svc", mdbName, index)
}

// GetMonarchServiceDNS returns the in-cluster DNS name for a Monarch Service.
func GetMonarchServiceDNS(mdb *mdbv1.MongoDB, namespace string, index int) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", MonarchServiceName(mdb.Name, index), namespace)
}

// monarchLabels returns the labels for Monarch resources at the given index.
func monarchLabels(mdb *mdbv1.MongoDB, role string, index int) map[string]string {
	return map[string]string{
		"app":               fmt.Sprintf("monarch-%s", role),
		"mongodb":           mdb.Name,
		"monarch-component": role,
		"monarch-index":     fmt.Sprintf("%d", index),
	}
}

// MdbMonarchImageEnv is the env var for overriding the Monarch image (for dev/testing).
const MdbMonarchImageEnv = "MDB_MONARCH_IMAGE"

// monarchImage returns the image to use for Monarch.
// Priority: 1) spec.monarch.image, 2) MDB_MONARCH_IMAGE env var, 3) default
func monarchImage(spec *mdbv1.MonarchSpec) string {
	if spec.Image != "" {
		return spec.Image
	}
	if envImage := os.Getenv(MdbMonarchImageEnv); envImage != "" {
		return envImage
	}
	return defaultMonarchImage
}

// monarchRole returns "shipper" for active clusters, "injector" for standby.
func monarchRole(spec *mdbv1.MonarchSpec) string {
	if spec.Role == mdbv1.MonarchRoleActive {
		return "shipper"
	}
	return "injector"
}

// BuildMonarchDeployment creates a Deployment for a Monarch shipper (active) or injector (standby).
func BuildMonarchDeployment(mdb *mdbv1.MongoDB, namespace string, index int) *appsv1.Deployment {
	spec := mdb.Spec.Monarch
	role := monarchRole(spec)
	name := MonarchDeploymentName(mdb.Name, index)
	labels := monarchLabels(mdb, role, index)

	envVars := []corev1.EnvVar{
		{Name: "S3_BUCKET", Value: spec.S3BucketName},
		{Name: "AWS_REGION", Value: spec.AWSRegion},
		{Name: "CLUSTER_PREFIX", Value: spec.ClusterPrefix},
		{Name: "SHARD_ID", Value: fmt.Sprintf("%d", index)},
		{Name: "PORT", Value: fmt.Sprintf("%d", monarchReplicationPort)},
		{Name: "HEALTH_PORT", Value: fmt.Sprintf("%d", monarchHealthPort)},
		{Name: "MONARCH_API_PORT", Value: fmt.Sprintf("%d", monarchAPIPort)},
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.CredentialsSecretRef.Name},
					Key:                  "awsAccessKeyId",
				},
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.CredentialsSecretRef.Name},
					Key:                  "awsSecretAccessKey",
				},
			},
		},
	}

	if spec.S3BucketEndpoint != "" {
		envVars = append(envVars, corev1.EnvVar{Name: "S3_ENDPOINT", Value: spec.S3BucketEndpoint})
	}
	if spec.S3PathStyleAccess {
		envVars = append(envVars, corev1.EnvVar{Name: "S3_PATH_STYLE", Value: "true"})
	}

	command := []string{
		"monarch", role,
		"--healthApiEndpoint=0.0.0.0:8080",
		"--monarchApiEndpoint=0.0.0.0:1122",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: kube.BaseOwnerReference(mdb),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:    fmt.Sprintf("monarch-%s", role),
							Image:   monarchImage(spec),
							Command: command,
							Ports: []corev1.ContainerPort{
								{Name: "health", ContainerPort: monarchHealthPort, Protocol: corev1.ProtocolTCP},
								{Name: "replication", ContainerPort: monarchReplicationPort, Protocol: corev1.ProtocolTCP},
								{Name: "monarch-api", ContainerPort: monarchAPIPort, Protocol: corev1.ProtocolTCP},
							},
							Env: envVars,
						},
					},
				},
			},
		},
	}
}

// BuildMonarchService creates a Service that fronts a Monarch Deployment.
func BuildMonarchService(mdb *mdbv1.MongoDB, namespace string, index int) *corev1.Service {
	spec := mdb.Spec.Monarch
	role := monarchRole(spec)
	labels := monarchLabels(mdb, role, index)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            MonarchServiceName(mdb.Name, index),
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: kube.BaseOwnerReference(mdb),
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "health", Port: monarchHealthPort, TargetPort: intstr.FromInt32(monarchHealthPort), Protocol: corev1.ProtocolTCP},
				{Name: "replication", Port: monarchReplicationPort, TargetPort: intstr.FromInt32(monarchReplicationPort), Protocol: corev1.ProtocolTCP},
				{Name: "monarch-api", Port: monarchAPIPort, TargetPort: intstr.FromInt32(monarchAPIPort), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}
