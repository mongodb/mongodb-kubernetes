package mdb

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1"
)

func validRole() MongoDBRole {
	return MongoDBRole{
		Role: "role",
		Db:   "admin",
		Privileges: []Privilege{
			{Actions: []string{"insert"}, Resource: Resource{Db: new("admin"), Collection: new("foo")}},
		},
	}
}

func TestRoleIsCorrectlyConfigured(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*MongoDBRole)
		expectedError string
	}{
		{
			name: "valid role",
		},
		{
			name: "valid cluster privilege",
			mutate: func(r *MongoDBRole) {
				r.Privileges = []Privilege{{Actions: []string{"listDatabases"}, Resource: Resource{Cluster: new(true)}}}
			},
		},
		{
			name:          "empty role name",
			mutate:        func(r *MongoDBRole) { r.Role = "" },
			expectedError: "Cannot create a role with an empty name",
		},
		{
			name:          "empty db",
			mutate:        func(r *MongoDBRole) { r.Db = "" },
			expectedError: "Cannot create a role with an empty db",
		},
		{
			name:          "inherited role with an empty name",
			mutate:        func(r *MongoDBRole) { r.Roles = []InheritedRole{{Db: "admin"}} },
			expectedError: "Cannot inherit from a role with an empty name",
		},
		{
			name:          "inherited role with an empty db",
			mutate:        func(r *MongoDBRole) { r.Roles = []InheritedRole{{Role: "role"}} },
			expectedError: "Cannot inherit from a role with an empty db",
		},
		{
			name: "authentication restriction with an invalid clientSource",
			mutate: func(r *MongoDBRole) {
				r.AuthenticationRestrictions = []AuthenticationRestriction{{ClientSource: []string{"355.127.0.1"}}}
			},
			expectedError: "AuthenticationRestriction is invalid - clientSource 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
		},
		{
			name: "authentication restriction with an invalid serverAddress",
			mutate: func(r *MongoDBRole) {
				r.AuthenticationRestrictions = []AuthenticationRestriction{{ServerAddress: []string{"355.127.0.1"}}}
			},
			expectedError: "AuthenticationRestriction is invalid - serverAddress 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
		},
		{
			name: "cluster privilege combined with db and collection",
			mutate: func(r *MongoDBRole) {
				r.Privileges[0].Resource.Cluster = new(true)
			},
			expectedError: "Privilege is invalid - Cluster: true is not compatible with setting db/collection",
		},
		{
			name: "cluster privilege set to false",
			mutate: func(r *MongoDBRole) {
				r.Privileges = []Privilege{{Actions: []string{"insert"}, Resource: Resource{Cluster: new(false)}}}
			},
			expectedError: "Privilege is invalid - The only valid value for privilege.cluster, if set, is true",
		},
		{
			name:          "invalid db action",
			mutate:        func(r *MongoDBRole) { r.Privileges[0].Actions = []string{"insertFoo"} },
			expectedError: "Privilege is invalid - Actions are not valid - insertFoo is not a valid db action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			role := validRole()
			if tt.mutate != nil {
				tt.mutate(&role)
			}
			expected := v1.ValidationSuccess()
			if tt.expectedError != "" {
				expected = v1.ValidationError("%s", tt.expectedError)
			}
			require.Equal(t, expected, RoleIsCorrectlyConfigured(role, communityMongoVersion))
		})
	}
}

func TestRolesAttributeIsCorrectlyConfigured(t *testing.T) {
	role := validRole()
	role.Db = ""

	rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetRoles([]MongoDBRole{role}).Build()
	require.EqualError(t, rs.ProcessValidationsOnReconcile(nil), "Error validating role - Cannot create a role with an empty db")
}
