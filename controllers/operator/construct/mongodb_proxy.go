package construct

import (
	"go.uber.org/zap"

	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"

	"github.com/mongodb/mongodb-kubernetes/pkg/util/architectures"
)

// In this mode mongod/mongos run in the `mongod` container as children of the mongodb-proxy, and the agent in
// the `mongodb-agent` container drives them over HTTP on localhost (see the agent flag -mongodProxyAddress).
// The two containers no longer share a process namespace.
const (
	// MongodbProxyAnnotation turns the mode on for one resource. It only takes effect together with the
	// static architecture and a configured proxy image (MDB_MONGODB_PROXY_IMAGE).
	MongodbProxyAnnotation = "mongodb.com/v1.mongodbProxy"

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

	// Environment variables of the agent container, read by agent-launcher.sh and by the scripts of the agent
	// image (setup-agent-files.sh and the probes).
	mongodbProxyAddressEnv = "MDB_MONGODB_PROXY_ADDRESS"
	mongodRootEnv          = "MDB_MONGOD_ROOT"
	initRootEnv            = "MDB_INIT_ROOT"
)

// usesMongodbProxy reports whether the StatefulSet runs mongod/mongos through the proxy.
func (d DatabaseStatefulSetOptions) usesMongodbProxy(mdb databaseStatefulSetSource) bool {
	return mdb.GetAnnotations()[MongodbProxyAnnotation] == "true" &&
		architectures.IsRunningStaticArchitecture(mdb.GetAnnotations(), d.DefaultArchitecture) &&
		d.MongodbProxyImage != ""
}

// warnIfMongodbProxyIsIgnored logs the cases in which the annotation is set but the StatefulSet is built
// without the proxy, because there is no error path to report them on.
func (d DatabaseStatefulSetOptions) warnIfMongodbProxyIsIgnored(mdb databaseStatefulSetSource, log *zap.SugaredLogger) {
	if mdb.GetAnnotations()[MongodbProxyAnnotation] != "true" || d.usesMongodbProxy(mdb) {
		return
	}
	if !architectures.IsRunningStaticArchitecture(mdb.GetAnnotations(), d.DefaultArchitecture) {
		log.Warnf("Annotation %s is set on %s, but the proxy needs the static architecture. mongod runs without the proxy", MongodbProxyAnnotation, mdb.GetName())
		return
	}
	log.Warnf("Annotation %s is set on %s, but the proxy image is not configured (MDB_MONGODB_PROXY_IMAGE). mongod runs without the proxy", MongodbProxyAnnotation, mdb.GetName())
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
