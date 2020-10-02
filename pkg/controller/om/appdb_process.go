package om

import (
	"fmt"
	"path"

	mdbv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/mdb"
	omv1 "github.com/10gen/ops-manager-kubernetes/pkg/apis/mongodb.com/v1/om"
	"github.com/10gen/ops-manager-kubernetes/pkg/util"
)

// TODO this file is intentionally created separately and duplicates the functions without "AppDB" suffix
// This is intended for the Ops Manager alpha, later the proper abstractions refacroring should be done
// (when all MongoDB fields are supported for AppDB):
// - both MongoDBSpec and AppDB objects implement the same interface to get access/mutate the same fields
// - both MongoDBSpec and AppDB include some common struct with all fields and all methods implementation. This is the
// same trick as including 'metav1.ObjectMeta' into 'MongoDB' which allows to implement 'meta.v1.Object' automatically
// - after this all the Operator/OpsManager methods dealing with 'mongodb.MongoDB' can be switched to this new interface

// NewMongodProcess
func NewMongodProcessAppDB(name, hostName string, appdb omv1.AppDB) Process {
	p := createProcess(
		WithName(name),
		WithHostname(hostName),
		WithProcessType(ProcessTypeMongod),
		WithAdditionalMongodConfig(appdb.MongoDbSpec.AdditionalMongodConfig),
		WithResourceSpec(appdb.MongoDbSpec),
	)

	if appdb.GetTlsCertificatesSecretName() != "" {
		certFile := fmt.Sprintf("%s/certs/%s-pem", util.SecretVolumeMountPath, name)

		// Process for AppDB use the mounted cert in-place and are not required for the certs to be
		// linked into a given location.
		p.ConfigureTLS(mdbv1.RequireSSLMode, certFile)
	}

	// default values for configurable values
	p.SetDbPath("/data")
	// CLOUDP-33467: we put mongod logs to the same directory as AA/Monitoring/Backup ones to provide single mount point
	// for all types of logs
	p.SetLogPath(path.Join(util.PvcMountPathLogs, "mongodb.log"))

	return p
}
