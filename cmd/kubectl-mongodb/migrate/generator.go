package migrate

import (
	"github.com/spf13/cast"
	"golang.org/x/xerrors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/v1/mdb"
	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/automationconfig"
)

type GenerateOptions struct {
	ResourceName          string
	CredentialsSecretName string
	ConfigMapName         string
}

// These structs intentionally use named fields instead of embedding
// metav1.TypeMeta/ObjectMeta to prevent controller-gen from treating
// them as CRD root types and generating an empty _.yaml skeleton.

type mongoDBCROutput struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec       mdbv1.MongoDbSpec `json:"spec"`
}

type mongoDBUserCROutput struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Metadata   metav1.ObjectMeta      `json:"metadata,omitempty"`
	Spec       userv1.MongoDBUserSpec `json:"spec"`
}

type UserCROutput struct {
	YAML           string
	Username       string
	Database       string
	NeedsPassword  bool
	PasswordSecret string
}

func GenerateMongoDBCR(ac *om.AutomationConfig, opts GenerateOptions) (string, string, error) {
	rs := findReplicaSet(ac.Deployment)
	if rs == nil {
		return "", "", xerrors.Errorf("no replica set found in the automation config")
	}

	rsName := cast.ToString(rs["_id"])
	members := getSlice(rs, "members")
	processes := getSlice(ac.Deployment, "processes")
	processMap := buildProcessMap(processes)
	externalMembers, version, fcv := extractMemberInfo(members, processMap)

	resourceName := opts.ResourceName
	if resourceName == "" {
		resourceName = rsName
	}

	objectMeta := metav1.ObjectMeta{
		Name: resourceName,
		Annotations: map[string]string{
			"mongodb.com/tool-version": pluginVersion(),
		},
	}
	spec := buildMongoDBSpec(version, fcv, len(externalMembers), opts, ac.Auth, processMap, members)

	yamlBytes, err := yaml.Marshal(mongoDBCROutput{
		APIVersion: "mongodb.com/v1",
		Kind:       "MongoDB",
		Metadata:   objectMeta,
		Spec:       spec,
	})
	if err != nil {
		return "", "", xerrors.Errorf("error marshalling CR to YAML: %w", err)
	}

	output := string(yamlBytes)
	output = appendExternalMembersComment(output, externalMembers)

	return output, resourceName, nil
}

func buildMongoDBSpec(
	version, fcv string,
	memberCount int,
	opts GenerateOptions,
	auth *om.Auth,
	processMap map[string]map[string]interface{},
	members []interface{},
) mdbv1.MongoDbSpec {
	spec := mdbv1.MongoDbSpec{
		DbCommonSpec: mdbv1.DbCommonSpec{
			Version:      version,
			ResourceType: mdbv1.ReplicaSet,
			ConnectionSpec: mdbv1.ConnectionSpec{
				SharedConnectionSpec: mdbv1.SharedConnectionSpec{
					OpsManagerConfig: &mdbv1.PrivateCloudConfig{
						ConfigMapRef: mdbv1.ConfigMapRef{
							Name: opts.ConfigMapName,
						},
					},
				},
				Credentials: opts.CredentialsSecretName,
			},
		},
		Members: memberCount,
	}

	if fcv != "" {
		spec.FeatureCompatibilityVersion = &fcv
	}

	spec.MemberConfig = buildZeroVoteMemberConfig(memberCount)
	spec.Security = inferSecurity(auth, processMap, members)
	spec.AdditionalMongodConfig = extractAdditionalMongodConfig(processMap, members)

	return spec
}

func buildZeroVoteMemberConfig(count int) []automationconfig.MemberOptions {
	config := make([]automationconfig.MemberOptions, count)
	for i := range config {
		v, p := 0, "0"
		config[i] = automationconfig.MemberOptions{
			Votes:    &v,
			Priority: &p,
		}
	}
	return config
}

func GenerateUserCRs(ac *om.AutomationConfig, mongodbResourceName string) ([]UserCROutput, error) {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil, nil
	}

	var results []UserCROutput
	for _, user := range ac.Auth.Users {
		if user == nil || user.Username == "" {
			continue
		}

		if user.Username == ac.Auth.AutoUser && user.Database == "admin" {
			continue
		}

		needsPassword := user.Database != "$external"
		crName := normalizeK8sName(user.Username)
		passwordSecretName := crName + "-password"

		spec := userv1.MongoDBUserSpec{
			Username: user.Username,
			Database: user.Database,
			MongoDBResourceRef: userv1.MongoDBResourceRef{
				Name: mongodbResourceName,
			},
			Roles: convertRoles(user.Roles),
		}

		if needsPassword {
			spec.PasswordSecretKeyRef = userv1.SecretKeyRef{
				Name: passwordSecretName,
				Key:  "password",
			}
		}

		yamlBytes, err := yaml.Marshal(mongoDBUserCROutput{
			APIVersion: "mongodb.com/v1",
			Kind:       "MongoDBUser",
			Metadata:   metav1.ObjectMeta{Name: crName},
			Spec:       spec,
		})
		if err != nil {
			return nil, xerrors.Errorf("error marshalling MongoDBUser CR for %q: %w", user.Username, err)
		}

		results = append(results, UserCROutput{
			YAML:           string(yamlBytes),
			Username:       user.Username,
			Database:       user.Database,
			NeedsPassword:  needsPassword,
			PasswordSecret: passwordSecretName,
		})
	}

	return results, nil
}

func convertRoles(roles []*om.Role) []userv1.Role {
	var out []userv1.Role
	for _, r := range roles {
		if r == nil {
			continue
		}
		out = append(out, userv1.Role{
			RoleName: r.Role,
			Database: r.Database,
		})
	}
	return out
}
