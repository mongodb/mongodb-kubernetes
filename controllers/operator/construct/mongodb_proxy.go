package construct

import (
	"path/filepath"
	"slices"

	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"
)

// In this mode mongod/mongos run in the `mongod` container as children of the mongodb-proxy, and the agent in
// the `mongodb-agent` container drives them over HTTP on a Unix socket (see the agent flag -mongodProxyAddress).
// The two containers no longer share a process namespace.
const (
	// MongodbProxyAddress is where the proxy listens: a Unix domain socket on a volume that only the agent and the
	// proxy containers mount. The proxy creates it with mode 0600, so the other containers of the pod (a sidecar of
	// the user, for example) cannot start or stop the database. The agent is told the same address.
	MongodbProxyAddress = "unix://" + mongodbProxySocketPath

	mongodbProxySocketVolumeName = "mongodb-proxy-socket"
	mongodbProxySocketDir        = "/var/run/mongodb-proxy"
	mongodbProxySocketPath       = mongodbProxySocketDir + "/proxy.sock"

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

	// The agent runs the launcher and the probes from the mounted init-database image.
	staticGAAgentLauncherPath  = mongodbInitDatabaseMountPath + "/scripts/agent-launcher.sh"
	staticGAProbePath          = mongodbInitDatabaseMountPath + "/probes/probe.sh"
	staticGAReadinessProbePath = mongodbInitDatabaseMountPath + "/probes/readinessprobe"

	// Environment variables of the agent container, read by agent-launcher.sh and by the scripts of the agent
	// image (setup-agent-files.sh and the probes).
	mongodbProxyAddressEnv = "MDB_MONGODB_PROXY_ADDRESS"
	mongodRootEnv          = "MDB_MONGOD_ROOT"
	initRootEnv            = "MDB_INIT_ROOT"

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
				PullPolicy: corev1.PullIfNotPresent,
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

// writableMountPaths returns the mount paths that hold files the agent writes: the mounts backed by an EmptyDir or a
// PersistentVolumeClaim. The proxy may read a config file only from these, because a Secret, a ConfigMap and an image
// carry files that no agent wrote and that a caller must not be able to read through the proxy.
func writableMountPaths(volumes []corev1.Volume, mounts []corev1.VolumeMount) []string {
	writable := map[string]bool{}
	for _, volume := range volumes {
		if volume.EmptyDir != nil || volume.PersistentVolumeClaim != nil {
			writable[volume.Name] = true
		}
	}

	var dirs []string
	for _, mount := range mounts {
		if mount.ReadOnly || !writable[mount.Name] {
			continue
		}
		dirs = append(dirs, filepath.Clean(mount.MountPath))
	}

	slices.Sort(dirs)
	return slices.Compact(dirs)
}

// mongodbProxyConfigDirs returns every directory the proxy may read a config file from. It derives them from the
// mounts of the database pod, so a new writable mount is covered without a change here, and a custom downloadBase
// moves the directory with the mount. The non-persistent constructors are the source of the paths. The persistent
// path installs claims with the same volume names and mount paths.
func mongodbProxyConfigDirs(opts DatabaseStatefulSetOptions) []string {
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	volumes, mounts = GetNonPersistentMongoDBVolumeMounts(volumes, mounts)
	volumes, mounts = GetNonPersistentAgentVolumeMounts(volumes, mounts, opts.GetDownloadBase())

	return writableMountPaths(volumes, mounts)
}
