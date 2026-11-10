package construct

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
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

	// The init container copies the init-database image into /opt/scripts. The agent runs the launcher from that
	// directory, so it needs no image volume and no /proc/<pid>/root lookup.
	staticGAAgentLauncherPath  = "/opt/scripts/agent-launcher.sh"
	staticGAProbePath          = "/opt/scripts/probe.sh"
	staticGAReadinessProbePath = "/opt/scripts/readinessprobe"

	// The agent image carries the database tools in /tools. The proxy owns mongod/mongos in static-ga, so the
	// agent needs only the tools and no MongoDB image volume.
	staticGAAgentToolsPath = "/tools"

	// Environment variables of the agent container.
	mongodbProxyAddressEnv = "MDB_MONGODB_PROXY_ADDRESS"
	mongodProxyAddressFlag = "mongodProxyAddress"
	binariesFixedPathFlag  = "binariesFixedPath"
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

func readOnlyMount(name, path string) corev1.VolumeMount {
	return corev1.VolumeMount{Name: name, MountPath: path, ReadOnly: true}
}
