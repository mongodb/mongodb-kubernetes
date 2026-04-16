package migratetomck

import (
	"fmt"
	"os"

	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	userv1 "github.com/mongodb/mongodb-kubernetes/api/v1/user"
	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
)

// GenerateUserCRs creates MongoDBUser CRs for each user in the automation config, skipping the agent user.
// When ExistingUserSecrets is set, each SCRAM user references a pre-created Secret, otherwise new Secrets
// are generated from UserPasswords. External users (X509 and LDAP) never produce a Secret.
// Output is ordered with each MongoDBUser followed by its Secret when one is generated.
func GenerateUserCRs(ac *om.AutomationConfig, mongodbResourceName, namespace string, opts GenerateOptions) ([]client.Object, error) {
	if ac.Auth == nil || len(ac.Auth.Users) == 0 {
		return nil, nil
	}

	crNameToUsername := map[string]string{}
	var results []client.Object
	for i, user := range ac.Auth.Users {
		if user == nil {
			return nil, fmt.Errorf("user at index %d is nil", i)
		}
		if user.Username == "" {
			return nil, fmt.Errorf("user at index %d has an empty username", i)
		}

		if user.Username == ac.Auth.AutoUser && user.Database == util.DefaultUserDatabase {
			continue
		}

		crName := userv1.NormalizeName(user.Username)
		if crName == "" {
			return nil, fmt.Errorf("username %q cannot be normalized to a valid Kubernetes name: no alphanumeric characters", user.Username)
		}
		if prev, exists := crNameToUsername[crName]; exists {
			return nil, fmt.Errorf("users %q and %q normalize to the same Kubernetes name %q; rename one before migration", prev, user.Username, crName)
		}
		crNameToUsername[crName] = user.Username

		roles, err := convertRoles(user.Roles)
		if err != nil {
			return nil, fmt.Errorf("failed to convert roles for user %q: %w", user.Username, err)
		}

		spec := userv1.MongoDBUserSpec{
			Username: user.Username,
			Database: user.Database,
			MongoDBResourceRef: userv1.MongoDBResourceRef{
				Name: mongodbResourceName,
			},
			Roles: roles,
		}

		var passwordSecret *corev1.Secret
		if user.Database != externalDatabase {
			if opts.ExistingUserSecrets != nil {
				sName, ok := opts.ExistingUserSecrets[userKey(user.Username, user.Database)]
				if !ok {
					fmt.Fprintf(os.Stderr, "[WARNING] skipping user %q (db: %s): not found in --users-secrets-file\n", user.Username, user.Database)
					continue
				}
				spec.PasswordSecretKeyRef = userv1.SecretKeyRef{Name: sName, Key: passwordSecretDataKey}
			} else {
				passwordSecretName := crName + "-password"
				if errs := k8svalidation.IsDNS1123Subdomain(passwordSecretName); len(errs) > 0 {
					return nil, fmt.Errorf("generated password Secret name %q is not a valid Kubernetes name; rename user %q before migration: %s", passwordSecretName, user.Username, errs[0])
				}
				password, ok := opts.UserPasswords[userKey(user.Username, user.Database)]
				if !ok {
					fmt.Fprintf(os.Stderr, "[WARNING] skipping user %q (db: %s): no password provided\n", user.Username, user.Database)
					continue
				}
				spec.PasswordSecretKeyRef = userv1.SecretKeyRef{Name: passwordSecretName, Key: passwordSecretDataKey}
				passwordSecret = GeneratePasswordSecret(passwordSecretName, namespace, password)
			}
		}

		results = append(results, &userv1.MongoDBUser{
			TypeMeta:   metav1.TypeMeta{APIVersion: "mongodb.com/v1", Kind: "MongoDBUser"},
			ObjectMeta: metav1.ObjectMeta{Name: crName, Namespace: namespace},
			Spec:       spec,
		})
		if passwordSecret != nil {
			results = append(results, passwordSecret)
		}
	}

	return results, nil
}

func convertRoles(roles []*om.Role) ([]userv1.Role, error) {
	var out []userv1.Role
	for i, r := range roles {
		if r == nil {
			return nil, fmt.Errorf("role at index %d is nil", i)
		}
		out = append(out, userv1.Role{
			RoleName: r.Role,
			Database: r.Database,
		})
	}
	return out, nil
}
