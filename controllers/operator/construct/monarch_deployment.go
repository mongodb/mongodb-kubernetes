package construct

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
	monarchpkg "github.com/mongodb/mongodb-kubernetes/pkg/monarch"
)

// injectMongodCredsIntoURI inserts "user:pass@" between "mongodb://" and the host
// list of a connection string and ensures authSource=admin is set. Used to embed
// the OM-managed mms-shipper credentials into both srcURI (RS-form, for oplog
// tailing) and backupMongoNodeURI (direct connection, for FCBIS snapshots).
// Empty user is a no-op so injectors (which read from S3, not mongod) work too.
func injectMongodCredsIntoURI(uri, user, password string) string {
	if user == "" {
		return uri
	}
	const scheme = "mongodb://"
	if !strings.HasPrefix(uri, scheme) {
		return uri
	}
	authPrefix := url.PathEscape(user) + ":" + url.PathEscape(password) + "@"
	withAuth := scheme + authPrefix + strings.TrimPrefix(uri, scheme)
	if strings.Contains(withAuth, "authSource=") {
		return withAuth
	}
	if strings.Contains(withAuth, "?") {
		return withAuth + "&authSource=admin"
	}
	return withAuth + "/?authSource=admin"
}

const (
	defaultMonarchImage = "268558157000.dkr.ecr.us-east-1.amazonaws.com/staging/mongodb-kubernetes-monarch-injector:latest"

	// DefaultMonarchReplicas is the default number of Monarch instances (shippers or injectors).
	// Multiple instances provide redundancy - the CAS protocol on S3 ensures only one write
	// per oplog entry is accepted, so duplicates are safe.
	DefaultMonarchReplicas = 3

	monarchConfigVolumeName = "monarch-config"
	monarchConfigMountPath  = "/etc/monarch"
	monarchConfigFileName   = "config.yaml"
)

// MonarchDeploymentName returns the name for the Monarch Deployment.
// Role is "shipper" for active clusters, "injector" for standby.
func MonarchDeploymentName(mdbName, role string) string {
	return fmt.Sprintf("%s-monarch-%s", mdbName, role)
}

// MonarchServiceName returns the name for the Monarch Service.
func MonarchServiceName(mdbName, role string) string {
	return fmt.Sprintf("%s-monarch-%s-svc", mdbName, role)
}

// MonarchConfigSecretName returns the name for the Monarch config Secret.
// The config is stored in a Secret (not ConfigMap) because it may contain
// mms-shipper credentials embedded in the srcURI.
func MonarchConfigSecretName(mdbName, role string) string {
	return fmt.Sprintf("%s-monarch-%s-config", mdbName, role)
}

// GetMonarchServiceDNS returns the in-cluster DNS name for the Monarch Service.
func GetMonarchServiceDNS(mdbName, role, namespace, clusterDomain string) string {
	return fmt.Sprintf("%s.%s.svc.%s", MonarchServiceName(mdbName, role), namespace, clusterDomain)
}

// monarchLabels returns the labels for Monarch resources.
func monarchLabels(mdb *mdbv1.MongoDB, role string) map[string]string {
	return map[string]string{
		"app":               fmt.Sprintf("monarch-%s", role),
		"mongodb":           mdb.Name,
		"monarch-component": role,
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

// MonarchSecretsMountDir is where the consolidated Monarch secrets bundle is
// mounted inside the shipper/injector container. Holds the cluster keyfile
// today and (future) TLS member certs / CA bundles. Each Secret key becomes a
// file at this path.
const MonarchSecretsMountDir = "/var/lib/mongodb-mms-automation"

// MonarchKeyfilePath is the on-disk path of the cluster keyfile. Matches mongod's
// own keyfile path so the injector/shipper participate in intra-cluster auth.
// (The injector advertises itself to mongod over port 9995; mongod heartbeats
// use keyfile auth.)
const MonarchKeyfilePath = MonarchSecretsMountDir + "/keyfile"

// monarchSecretsVolumeName is the Pod-level volume name for the Secret mount.
const monarchSecretsVolumeName = "monarch-secrets"

// BuildMonarchConfigSecret creates a Secret with the Monarch YAML configuration.
// The config is stored in a Secret (not ConfigMap) because it contains mms-shipper
// credentials embedded in the srcURI for the shipper role.
//
// mongodUsername/Password are the OM-managed mms-shipper credentials (read by the
// caller from the agent-API AC) embedded into srcURI and backupMongoNodeURI so the
// shipper can authenticate to mongod for oplog tailing and $_backupFile (FCBIS).
// Both empty for the injector path.
//
// monarchSecretsName is the K8s Secret holding the Monarch secrets bundle
// (cluster keyfile today; TLS member cert / CA in the future).
// Empty when SCRAM is disabled — the YAML omits securityKeyFilePath in that case.
func BuildMonarchConfigSecret(mdb *mdbv1.MongoDB, namespace string, srcURI string, mongodUsername, mongodPassword string, monarchSecretsName string) *corev1.Secret {
	spec := mdb.Spec.Monarch
	role := monarchRole(spec)
	labels := monarchLabels(mdb, role)

	// Build YAML config - monarch reads this via --config flag
	// Build per-member host list: <rs>-0.<svc>.<ns>.svc.<clusterDomain>:27017,...
	svcName := mdb.ServiceName()
	clusterDomain := mdb.Spec.GetClusterDomain()
	hosts := make([]string, mdb.Spec.Members)
	for i := 0; i < mdb.Spec.Members; i++ {
		hosts[i] = fmt.Sprintf("%s-%d.%s.%s.svc.%s:27017", mdb.Name, i, svcName, namespace, clusterDomain)
	}

	replSetHostsYAML := ""
	for _, h := range hosts {
		replSetHostsYAML += fmt.Sprintf("\n  - \"%s\"", h)
	}

	// The shipper binary defaults to oplog-only mode. We need it to also produce
	// FCBIS snapshots so the standby's agent can DownloadFCBIS during ProvisionStandby.
	// The injector binary has no mode flag.
	modeLine := ""
	if role == "shipper" {
		modeLine = "\nmode: shipperAndSnapshotter"
	}

	// The shipper writes the cluster manifest (failoverdemo/cluster_manifest_v1.bson)
	// during init when clusterStore.initialize=true; the standby's injector reads
	// it on every find request to know which shards exist. Without this block the
	// shipper logs `clusterStore.initialize=false` and the injector 404s.
	// Single-shard for now ("0"); the cluster prefix doubles as the cluster ID
	// since each MongoDB CR maps 1:1 to a Monarch DR pair.
	clusterStoreBlock := ""
	if role == "shipper" {
		// shardDriftLimit must be > 0 (the binary refuses to create the cluster
		// manifest otherwise: 'shardDriftLimit must be greater than 0'). For
		// single-shard deployments drift is moot — pick a sensible default that
		// matches what OM uses in non-K8s deployments.
		clusterStoreBlock = fmt.Sprintf(`
clusterStore:
  initialize: true
  clusterId: "%s"
  allShardIds: ["0"]
  shardDriftLimit: 60`, spec.S3.GetPrefix(mdb.Name))
	}

	// The snapshotter sub-component requires a direct-connection URI (it rejects
	// any connection string with replicaSet=). Point it at member-0 directly.
	backupMongoNodeURI := fmt.Sprintf("mongodb://%s/?directConnection=true&connectTimeoutMS=20000&serverSelectionTimeoutMS=20000", hosts[0])

	// Embed mms-shipper credentials when present (only for the active shipper).
	// The config is stored in a Secret (not ConfigMap) so credentials are protected
	// by Kubernetes RBAC and encryption-at-rest defaults.
	srcURI = injectMongodCredsIntoURI(srcURI, mongodUsername, mongodPassword)
	backupMongoNodeURI = injectMongodCredsIntoURI(backupMongoNodeURI, mongodUsername, mongodPassword)

	// AWS credentials use credentialsChain mode (reads from AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY env vars)
	config := fmt.Sprintf(`# Monarch %s configuration - generated by mongodb-kubernetes-operator
clusterPrefix: %s
shardId: "0"
replSetName: "%s"
replSetHosts:%s%s

srcURI: "%s"
backupMongoNodeURI: "%s"
%s

aws:
  authMode: credentialsChain
  bucketName: %s
  region: %s`,
		role,
		spec.S3.GetPrefix(mdb.Name),
		mdb.Name,
		replSetHostsYAML,
		modeLine,
		srcURI,
		backupMongoNodeURI,
		clusterStoreBlock,
		spec.S3.Bucket,
		spec.S3.Region,
	)

	// Add optional S3 endpoint config for MinIO/LocalStack
	if spec.S3.Endpoint != "" {
		config += fmt.Sprintf(`
  customBaseEndpoint: %s`, spec.S3.Endpoint)
	}
	if spec.S3.PathStyle {
		config += `
  usePathStyle: true`
	}

	// Bind all ports to 0.0.0.0 so the Kubernetes Service can route traffic to pods
	config += fmt.Sprintf(`

bindIp: "0.0.0.0"
port: %d
healthApiEndpoint: "0.0.0.0:%d"
monarchApiEndpoint: "0.0.0.0:%d"
`, monarchpkg.ReplicationPort, monarchpkg.HealthPort, monarchpkg.APIPort)

	// Injector-only: populate mongodURIs, injectorHosts, runCoordinator.
	//
	// The ShardReplStatusUpdater connects to each mongod via mongodURIs to compute
	// earliestSafeTimestamp and write shard_repl_status_v1.bson to S3. The Coordinator
	// (runCoordinator: true) reads that file to advance the cluster repl status.
	// Without mongodURIs the updater never starts → the file is never written → the
	// coordinator 404s on every iteration → DR state never advances past PromoteStandby.
	//
	// URI format: `mongodb://host:port/` — bare URI, exactly one host, no query
	// options, no credentials. Per the injector binary's own --mongodURIs help text:
	// "each URI must contain exactly one host and no query options — credentials
	// are not allowed in the URI (auth uses securityKeyFile when set, or noauth
	// when omitted)". Auth is resolved internally via --securityKeyFile.
	//
	// injectorHosts is the intra-shard injector mesh (single Service DNS entry — the
	// Service load-balances across pods). runCoordinator=true starts the goroutine
	// that drives the PromoteStandby → StandbyReadyToPromote → Active S3 transition.
	if role == "injector" {
		injectorSvcDNS := GetMonarchServiceDNS(mdb.Name, role, namespace, clusterDomain)
		mongodURIsYAML := ""
		for _, h := range hosts {
			mongodURIsYAML += fmt.Sprintf("\n  - \"mongodb://%s/\"", h)
		}
		config += fmt.Sprintf(`mongodURIs:%s
injectorHosts:
  - "%s:%d"
runCoordinator: true
`, mongodURIsYAML, injectorSvcDNS, monarchpkg.ReplicationPort)
	}

	// Cluster keyfile path (intra-cluster auth). Empty Secret name => SCRAM is
	// disabled and the YAML omits the line so the injector/shipper binary
	// doesn't try to open a non-existent file.
	// Note: the YAML field is `securityKeyFile`, NOT `securityKeyFilePath`. The
	// injector/shipper binaries parse the former (see oploginjector/maintainer.go's
	// cfg["securityKeyFile"]). Path-suffix is only the operator-side variable name.
	if monarchSecretsName != "" {
		config += fmt.Sprintf("securityKeyFile: %s\n", MonarchKeyfilePath)
	}

	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:            MonarchConfigSecretName(mdb.Name, role),
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: kube.BaseOwnerReference(mdb),
		},
		Data: map[string][]byte{
			monarchConfigFileName: []byte(config),
		},
	}
}

// monarchVolumeMounts returns the VolumeMount list for the monarch container.
// When monarchSecretsName is non-empty, also mount the secrets bundle at
// MonarchSecretsMountDir (read-only) so the binary can read the keyfile etc.
func monarchVolumeMounts(monarchSecretsName string) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		{Name: monarchConfigVolumeName, MountPath: monarchConfigMountPath, ReadOnly: true},
	}
	if monarchSecretsName != "" {
		mounts = append(mounts, corev1.VolumeMount{
			Name: monarchSecretsVolumeName, MountPath: MonarchSecretsMountDir, ReadOnly: true,
		})
	}
	return mounts
}

// monarchVolumes returns the Pod-level Volume list. When monarchSecretsName is
// non-empty, includes a Secret volume with mode 0600 (matches mongod's keyfile
// permissions; required for SCRAM-SHA / keyfile auth to be accepted).
func monarchVolumes(mdbName, role, monarchSecretsName string) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: monarchConfigVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: MonarchConfigSecretName(mdbName, role),
				},
			},
		},
	}
	if monarchSecretsName != "" {
		mode := int32(0o600)
		volumes = append(volumes, corev1.Volume{
			Name: monarchSecretsVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  monarchSecretsName,
					DefaultMode: &mode,
				},
			},
		})
	}
	return volumes
}

// BuildMonarchDeployment creates a Deployment for Monarch shipper (active) or injector (standby).
// The deployment has multiple replicas for redundancy.
//
// monarchSecretsName, when non-empty, is a K8s Secret mounted at
// MonarchSecretsMountDir holding the cluster keyfile (and future TLS material).
func BuildMonarchDeployment(mdb *mdbv1.MongoDB, namespace string, monarchSecretsName string) *appsv1.Deployment {
	spec := mdb.Spec.Monarch
	role := monarchRole(spec)
	name := MonarchDeploymentName(mdb.Name, role)
	labels := monarchLabels(mdb, role)

	// Command uses --config to read YAML configuration
	configPath := fmt.Sprintf("%s/%s", monarchConfigMountPath, monarchConfigFileName)
	command := []string{
		"monarch", role,
		fmt.Sprintf("--config=%s", configPath),
	}

	// AWS credentials via env vars (monarch reads these with credentialsChain auth mode)
	envVars := []corev1.EnvVar{
		{
			Name: "AWS_ACCESS_KEY_ID",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.S3.CredentialsSecretRef.Name},
					Key:                  "awsAccessKeyId",
				},
			},
		},
		{
			Name: "AWS_SECRET_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: spec.S3.CredentialsSecretRef.Name},
					Key:                  "awsSecretAccessKey",
				},
			},
		},
	}

	replicas := int32(DefaultMonarchReplicas)

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: kube.BaseOwnerReference(mdb),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(replicas),
			Selector: &metav1.LabelSelector{
				MatchLabels: labels,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
					// checksum/config is initialized empty and overwritten by the reconciler's
					// CreateOrUpdate mutate callback. Declaring it here ensures it is present
					// from the very first create, not only on subsequent updates.
					Annotations: map[string]string{
						"checksum/config": "",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:            fmt.Sprintf("monarch-%s", role),
							Image:           monarchImage(spec),
							ImagePullPolicy: corev1.PullAlways,
							Command:         command,
							Ports: []corev1.ContainerPort{
								{Name: "health", ContainerPort: monarchpkg.HealthPort, Protocol: corev1.ProtocolTCP},
								{Name: "replication", ContainerPort: monarchpkg.ReplicationPort, Protocol: corev1.ProtocolTCP},
								{Name: "monarch-api", ContainerPort: monarchpkg.APIPort, Protocol: corev1.ProtocolTCP},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{
										Path: "/api/v1/status",
										Port: intstr.FromInt32(monarchpkg.HealthPort),
									},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       10,
								FailureThreshold:    3,
							},
							Env:          envVars,
							VolumeMounts: monarchVolumeMounts(monarchSecretsName),
						},
					},
					Volumes: monarchVolumes(mdb.Name, role, monarchSecretsName),
				},
			},
		},
	}
}

// BuildMonarchService creates a Service that fronts the Monarch Deployment.
func BuildMonarchService(mdb *mdbv1.MongoDB, namespace string) *corev1.Service {
	spec := mdb.Spec.Monarch
	role := monarchRole(spec)
	labels := monarchLabels(mdb, role)

	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:            MonarchServiceName(mdb.Name, role),
			Namespace:       namespace,
			Labels:          labels,
			OwnerReferences: kube.BaseOwnerReference(mdb),
		},
		Spec: corev1.ServiceSpec{
			Selector: labels,
			Ports: []corev1.ServicePort{
				{Name: "health", Port: monarchpkg.HealthPort, TargetPort: intstr.FromInt32(monarchpkg.HealthPort), Protocol: corev1.ProtocolTCP},
				{Name: "replication", Port: monarchpkg.ReplicationPort, TargetPort: intstr.FromInt32(monarchpkg.ReplicationPort), Protocol: corev1.ProtocolTCP},
				{Name: "monarch-api", Port: monarchpkg.APIPort, TargetPort: intstr.FromInt32(monarchpkg.APIPort), Protocol: corev1.ProtocolTCP},
			},
		},
	}
}
