package role

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
)

type ClusterMongoDBRoleBuilder struct {
	name        string
	finalizers  []string
	annotations map[string]string
	mongoDBRole mdb.MongoDBRole
}

func DefaultClusterMongoDBRoleBuilder() *ClusterMongoDBRoleBuilder {
	return &ClusterMongoDBRoleBuilder{
		name:       "default-role",
		finalizers: []string{},
		mongoDBRole: mdb.MongoDBRole{
			Role:                       "default-role",
			AuthenticationRestrictions: nil,
			Db:                         "admin",
			Privileges:                 nil,
			Roles: []mdb.InheritedRole{
				{
					Role: "readWrite",
					Db:   "admin",
				},
			},
		},
		annotations: map[string]string{},
	}
}

func (b *ClusterMongoDBRoleBuilder) SetName(name string) *ClusterMongoDBRoleBuilder {
	b.name = name
	return b
}

func (b *ClusterMongoDBRoleBuilder) AddFinalizer(finalizer string) *ClusterMongoDBRoleBuilder {
	b.finalizers = append(b.finalizers, finalizer)
	return b
}

func (b *ClusterMongoDBRoleBuilder) SetMongoDBRole(role mdb.MongoDBRole) *ClusterMongoDBRoleBuilder {
	b.mongoDBRole = role
	return b
}

func (b *ClusterMongoDBRoleBuilder) AddAnnotation(key, value string) *ClusterMongoDBRoleBuilder {
	b.annotations[key] = value
	return b
}

func (b *ClusterMongoDBRoleBuilder) Build() *ClusterMongoDBRole {
	return &ClusterMongoDBRole{
		ObjectMeta: metav1.ObjectMeta{
			Name:        b.name,
			Finalizers:  b.finalizers,
			Annotations: b.annotations,
		},
		Spec: ClusterMongoDBRoleSpec{
			MongoDBRole: b.mongoDBRole,
		},
	}
}
