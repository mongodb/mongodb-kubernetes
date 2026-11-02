package construct

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// In this mode mongod/mongos run in the `mongod` container as children of the mongodb-proxy, and the agent in
// the `mongodb-agent` container drives them over HTTP on localhost (see the agent flag -mongodProxyAddress).
// The two containers no longer share a process namespace.
const (
	// MongodbProxyAddress is where the proxy listens. It is the default address of the proxy.
	MongodbProxyAddress = "127.0.0.1:8099"

	mongodbProxyVolumeName = "mongodb-proxy"
	// The proxy image is mounted here, read-only, and contains only the binary.
	mongodbProxyMountPath  = "/mnt/proxy"
	mongodbProxyBinaryPath = mongodbProxyMountPath + "/proxy"

	// The official MongoDB image is also mounted into the agent container. There is no shared process
	// namespace to reach the binaries through /proc/<pid>/root any more, and the agent still needs them to
	// find out which versions are installed and for the database tools.
	mongodbBinariesVolumeName = "mongodb-binaries"
	mongodbBinariesMountPath  = "/mnt/mongodb"

	// The init-database image is mounted into the agent container as well. Its scripts and probes used to be
	// reached through /proc/<pid>/root of the utilities container, which needs a shared process namespace.
	mongodbInitDatabaseVolumeName = "mongodb-init-database"
	mongodbInitDatabaseMountPath  = "/mnt/init-database"

	// Environment variables of the agent container, read by agent-launcher.sh and by the scripts of the agent
	// image (setup-agent-files.sh and the probes).
	mongodbProxyAddressEnv = "MDB_MONGODB_PROXY_ADDRESS"
	mongodRootEnv          = "MDB_MONGOD_ROOT"
	initRootEnv            = "MDB_INIT_ROOT"
)

// usesMongodbProxy reports whether the StatefulSet runs mongod/mongos through the proxy.
func (d DatabaseStatefulSetOptions) usesMongodbProxy(mdb databaseStatefulSetSource) bool {
	return architectures.IsRunningStaticGAArchitecture(mdb.GetAnnotations(), d.DefaultArchitecture)
}

func (d DatabaseStatefulSetOptions) ValidateProxyPrerequisites(name string, annotations map[string]string) error {
	return validateStaticGAProxyPrerequisites(
		name,
		annotations,
		d.DefaultArchitecture,
		d.MongodbProxyImage,
		d.MongodbImage,
		d.InitDatabaseImage,
		d.CustomAgentURL,
		d.ProxyAgentCompatibilityVerified,
	)
}

func validateStaticGAProxyPrerequisites(
	name string,
	annotations map[string]string,
	defaultArchitecture architectures.DefaultArchitecture,
	proxyImage string,
	mongodbImage string,
	initDatabaseImage string,
	customAgentURL string,
	agentCompatibilityVerified bool,
) error {
	if !architectures.IsRunningStaticGAArchitecture(annotations, defaultArchitecture) {
		return nil
	}
	if proxyImage == "" {
		return fmt.Errorf("static-ga workload %q requires a MongoDB proxy image", name)
	}
	if customAgentURL == "" {
		return fmt.Errorf("static-ga workload %q requires MDB_CUSTOM_AGENT_URL for the proxy-capable Agent", name)
	}
	if mongodbImage == "" || initDatabaseImage == "" {
		return fmt.Errorf("static-ga workload %q requires MongoDB and init-database images for Agent image volumes", name)
	}
	if !agentCompatibilityVerified {
		return fmt.Errorf("static-ga workload %q cannot roll out because Agent proxy compatibility is not verified", name)
	}
	return nil
}

func imageVolume(name, reference string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{
				Reference:  reference,
				PullPolicy: corev1.PullIfNotPresent,
			},
		},
	}
}

func readOnlyMount(name, path string) corev1.VolumeMount {
	return corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: true}
}
