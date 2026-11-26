package construct

import (
	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
)

// In this mode mongod/mongos run in the `mongod` container as children of the mongodb-proxy, and the agent in
// the `mongodb-agent` container drives them over HTTP on a Unix socket (see the agent flag -mongodProxyAddress).
// The two containers no longer share a process namespace.
const (
	// MongodbProxyAddress is a mode-0600 Unix socket mounted in the Agent and database containers by default.
	// Authors who can override the Pod template are trusted to control the database process.
	MongodbProxyAddress = "unix://" + mongodbProxySocketPath

	mongodbProxySocketVolumeName = "mongodb-proxy-socket"
	mongodbProxySocketDir        = "/var/run/mongodb-proxy"
	mongodbProxySocketPath       = mongodbProxySocketDir + "/proxy.sock"

	mongodbProxyVolumeName = "mongodb-proxy"
	// The proxy image is mounted here, read-only, and contains only the binary.
	mongodbProxyMountPath  = "/mnt/proxy"
	mongodbProxyBinaryPath = mongodbProxyMountPath + "/proxy"

	// Environment variables of the agent container, read by agent-launcher.sh and by the scripts of the agent
	// image (setup-agent-files.sh and the probes).
	mongodbProxyAddressEnv = "MDB_MONGODB_PROXY_ADDRESS"

	// The agent image carries the database tools in /tools. The proxy starts mongod, so the agent
	// needs no mongod path, only the tools path.
	staticGAAgentToolsPath = "/tools"
	mongodbToolsPathFlag   = "mongodbToolsPath"
)

func imageVolume(name, reference string) corev1.Volume {
	return corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			Image: &corev1.ImageVolumeSource{
				Reference:  reference,
				PullPolicy: corev1.PullPolicy(env.ReadOrPanic(util.ImagePullPolicyEnv)), // nolint:forbidigo
			},
		},
	}
}

// mongodbProxySocketVolume holds only the socket file.
func mongodbProxySocketVolume() corev1.Volume {
	limit := resource.MustParse("1Mi")
	return corev1.Volume{
		Name: mongodbProxySocketVolumeName,
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit},
		},
	}
}

func mongodbProxySocketMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: mongodbProxySocketVolumeName, MountPath: mongodbProxySocketDir}
}

func readOnlyMount(name, path string) corev1.VolumeMount {
	return corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: true}
}
