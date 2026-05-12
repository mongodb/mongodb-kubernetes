// Package construct holds shared constants used by both the Enterprise operator
// and MongoDB Community Operator when constructing StatefulSets.
package construct

// Environment variable names that control which container images are used for
// database and agent containers.  These constants are referenced by image-URL
// resolution helpers (pkg/images), all database-type controllers, and main.go.
const (
	MongodbRepoUrlEnv = "MONGODB_REPO_URL"
	MongodbImageEnv   = "MONGODB_IMAGE"
	AgentImageEnv     = "AGENT_IMAGE"

	MongoDBAssumeEnterpriseEnv = "MDB_ASSUME_ENTERPRISE"
)

// Container name constants used when locating containers within a PodSpec.
const (
	AgentName   = "mongodb-agent"
	MongodbName = "mongod"
)

// OfficialMongodbRepoUrls lists the canonical MongoDB container registries used
// to detect whether an image is an "official" MongoDB image.
var OfficialMongodbRepoUrls = []string{"docker.io/mongodb", "quay.io/mongodb"}
