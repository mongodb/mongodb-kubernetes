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
		role          MongoDBRole
		expectedError string
	}{
		{
			name: "valid role",
			role: validRole(),
		},
		{
			name: "valid cluster privilege",
			role: MongoDBRole{
				Role:       "role",
				Db:         "admin",
				Privileges: []Privilege{{Actions: []string{"listDatabases"}, Resource: Resource{Cluster: new(true)}}},
			},
		},
		{
			name:          "empty role name",
			role:          MongoDBRole{Db: "admin"},
			expectedError: "Cannot create a role with an empty name",
		},
		{
			name:          "empty db",
			role:          MongoDBRole{Role: "role"},
			expectedError: "Cannot create a role with an empty db",
		},
		{
			name:          "inherited role with an empty name",
			role:          MongoDBRole{Role: "role", Db: "admin", Roles: []InheritedRole{{Db: "admin"}}},
			expectedError: "Cannot inherit from a role with an empty name",
		},
		{
			name:          "inherited role with an empty db",
			role:          MongoDBRole{Role: "role", Db: "admin", Roles: []InheritedRole{{Role: "role"}}},
			expectedError: "Cannot inherit from a role with an empty db",
		},
		{
			name: "authentication restriction with an invalid clientSource",
			role: MongoDBRole{
				Role:                       "role",
				Db:                         "admin",
				AuthenticationRestrictions: []AuthenticationRestriction{{ClientSource: []string{"355.127.0.1"}}},
			},
			expectedError: "AuthenticationRestriction is invalid - clientSource 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
		},
		{
			name: "authentication restriction with an invalid serverAddress",
			role: MongoDBRole{
				Role:                       "role",
				Db:                         "admin",
				AuthenticationRestrictions: []AuthenticationRestriction{{ServerAddress: []string{"355.127.0.1"}}},
			},
			expectedError: "AuthenticationRestriction is invalid - serverAddress 355.127.0.1 is neither a valid IP address nor a valid CIDR range",
		},
		{
			name: "cluster privilege combined with db and collection",
			role: MongoDBRole{
				Role: "role",
				Db:   "admin",
				Privileges: []Privilege{
					{Actions: []string{"insert"}, Resource: Resource{Cluster: new(true), Db: new("admin"), Collection: new("foo")}},
				},
			},
			expectedError: "Privilege is invalid - Cluster: true is not compatible with setting db/collection",
		},
		{
			name: "cluster privilege set to false",
			role: MongoDBRole{
				Role:       "role",
				Db:         "admin",
				Privileges: []Privilege{{Actions: []string{"insert"}, Resource: Resource{Cluster: new(false)}}},
			},
			expectedError: "Privilege is invalid - The only valid value for privilege.cluster, if set, is true",
		},
		{
			name: "invalid db action",
			role: MongoDBRole{
				Role:       "role",
				Db:         "admin",
				Privileges: []Privilege{{Actions: []string{"insertFoo"}, Resource: Resource{Db: new("admin"), Collection: new("foo")}}},
			},
			expectedError: "Privilege is invalid - Actions are not valid - insertFoo is not a valid db action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expected := v1.ValidationSuccess()
			if tt.expectedError != "" {
				expected = v1.ValidationResult{Msg: tt.expectedError, Level: v1.ErrorLevel}
			}
			require.Equal(t, expected, RoleIsCorrectlyConfigured(tt.role, communityMongoVersion))
		})
	}
}

func TestRolesAttributeIsCorrectlyConfigured(t *testing.T) {
	role := validRole()
	role.Db = ""

	rs := NewReplicaSetBuilder().AddDummyOpsManagerConfig().SetRoles([]MongoDBRole{role}).Build()
	require.EqualError(t, rs.ProcessValidationsOnReconcile(nil), "Error validating role - Cannot create a role with an empty db")
}
