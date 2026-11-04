package certs

import (
	"context"
	"crypto/x509/pkix"
	"fmt"
	"maps"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/xerrors"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
	"github.com/mongodb/mongodb-kubernetes/controllers/operator/workflow"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/util"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/stringutil"
)

// VerifyMigrationSubjects checks that a migration to operator-managed certificates keeps the subjects
// of the certificates in use, and fails with the values to set when the spec would change them.
//
// MongoDB recognises the members of a deployment by the organization and organizational unit of their
// certificates, and registers the automation agent under the subject of its certificate. The
// certificates in use carry the subjects the user chose, while the managed certificates get the operator's
// defaults unless managedCertificate.subjects is configured. With different subjects, the members
// stop recognising each other and the agent stops authenticating while the pods restart one at a
// time, which takes the deployment down. That is why the migration has to keep the subjects it
// starts with.
func VerifyMigrationSubjects(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, lastAchieved *mdbv1.Security, stsName string, log *zap.SugaredLogger) workflow.Status {
	current := res.GetSecurity()
	var problems []string

	// The membership subject only matters when internal cluster authentication uses x509, because that
	// is when mongod compares the organization and organizational unit of the certificates.
	if current.GetInternalClusterAuthenticationMode() == util.X509 {
		legacy, found, status := readLegacySubject(ctx, c, res, lastAchieved.MemberCertificateSecretName(stsName))
		if !status.IsOK() {
			return status
		}
		if found {
			wanted := managedCertSubject(membershipSubject(res))
			if !legacy.sameMembership(wanted) {
				problems = append(problems, fmt.Sprintf(
					"the member certificate's subject would change from %s to %s, and MongoDB compares O and OU to recognise the members of a deployment. Set spec.security.managedCertificate.subjects.membership to %s to keep the subject in use today",
					legacy.membership(), wanted.membership(), legacy.membershipFields()))
			}
		}
	}

	// The agent's MongoDB user is registered under the whole subject of its certificate, so any change
	// means a different user, and the agents on the pods that have not restarted yet stop
	// authenticating.
	if current.GetAgentMechanism("") == util.X509 {
		legacy, found, status := readLegacySubject(ctx, c, res, lastAchieved.AgentClientCertificateSecretName(res.GetName()))
		if !status.IsOK() {
			return status
		}
		if found {
			wanted := managedCertSubject(agentSubject(res))
			if !legacy.sameIdentity(wanted) {
				problems = append(problems, fmt.Sprintf(
					"the agent certificate's subject would change from %s to %s, and the agent's MongoDB user is registered under its subject. Set spec.security.managedCertificate.subjects.agent to %s to keep the subject in use today",
					legacy.identity(), wanted.identity(), legacy.identityFields()))
			}
		}
	}

	if len(problems) > 0 {
		message := "this migration would change the subjects MongoDB uses to identify the deployment, so its members would stop recognising each other while they restart: " + strings.Join(problems, "; ")
		log.Warnf("Refusing the migration to operator-managed certificates: %s", message)
		return workflow.Failed(xerrors.New(message))
	}
	return workflow.OK()
}

// readLegacySubject reads the subject of a legacy certificate the user created. found is false when the
// secret is not there, which is normal for the certificates a deployment does not have, for example
// the clusterfile when internal cluster authentication is not x509.
func readLegacySubject(ctx context.Context, c kubernetesClient.Client, res CertificateOwner, secretName string) (certSubject, bool, workflow.Status) {
	subject, found, status := readCertSubject(ctx, c, res.GetNamespace(), secretName)
	if !status.IsOK() || !found {
		return certSubject{}, found, status
	}
	return certSubjectFromName(subject), true, workflow.OK()
}

// certSubject is a certificate subject in the shape of the managedCertificate.subjects fields.
type certSubject struct {
	commonName          string
	organizations       []string
	organizationalUnits []string
	countries           []string
	localities          []string
	provinces           []string
}

func certSubjectFromName(subject pkix.Name) certSubject {
	return certSubject{
		commonName:          subject.CommonName,
		organizations:       subject.Organization,
		organizationalUnits: subject.OrganizationalUnit,
		countries:           subject.Country,
		localities:          subject.Locality,
		provinces:           subject.Province,
	}
}

// managedCertSubject is the subject the operator would put on a certificate, from the subject and
// common name the managedCertificate fields resolve to.
func managedCertSubject(subject *certmanagerv1.X509Subject, commonName string) certSubject {
	if subject == nil {
		return certSubject{commonName: commonName}
	}
	return certSubject{
		commonName:          commonName,
		organizations:       subject.Organizations,
		organizationalUnits: subject.OrganizationalUnits,
		countries:           subject.Countries,
		localities:          subject.Localities,
		provinces:           subject.Provinces,
	}
}

// sameMembership reports whether two subjects match for MongoDB's membership check, which compares
// the organization and the organizational unit.
func (s certSubject) sameMembership(other certSubject) bool {
	return sameValues(s.organizations, other.organizations) && sameValues(s.organizationalUnits, other.organizationalUnits)
}

// sameIdentity reports whether two subjects are the same MongoDB user identity, which is the whole
// subject.
func (s certSubject) sameIdentity(other certSubject) bool {
	return s.commonName == other.commonName && s.sameMembership(other) &&
		sameValues(s.countries, other.countries) && sameValues(s.localities, other.localities) &&
		sameValues(s.provinces, other.provinces)
}

// sameValues reports whether two lists hold the same values, in any order.
func sameValues(a, b []string) bool {
	return maps.Equal(stringutil.ToSet(a), stringutil.ToSet(b))
}

// membership renders the attributes that decide membership, for a message.
func (s certSubject) membership() string {
	return fmt.Sprintf("O=%v, OU=%v", s.organizations, s.organizationalUnits)
}

// membershipFields renders the values to set in spec.security.managedCertificate.subjects.membership.
func (s certSubject) membershipFields() string {
	return fmt.Sprintf("organizations: %v, organizationalUnits: %v", s.organizations, s.organizationalUnits)
}

// identity renders the whole subject, for a message.
func (s certSubject) identity() string {
	parts := []string{}
	if s.commonName != "" {
		parts = append(parts, "CN="+s.commonName)
	}
	parts = append(parts, s.membership())
	parts = append(parts, s.optionalAttributes()...)
	return strings.Join(parts, ", ")
}

// identityFields renders the values to set in spec.security.managedCertificate.subjects.agent.
func (s certSubject) identityFields() string {
	parts := []string{}
	if s.commonName != "" {
		parts = append(parts, "commonName: "+s.commonName)
	}
	parts = append(parts, s.membershipFields())
	parts = append(parts, s.optionalAttributeFields()...)
	return strings.Join(parts, ", ")
}

func (s certSubject) optionalAttributes() []string {
	var parts []string
	if len(s.countries) > 0 {
		parts = append(parts, fmt.Sprintf("C=%v", s.countries))
	}
	if len(s.localities) > 0 {
		parts = append(parts, fmt.Sprintf("L=%v", s.localities))
	}
	if len(s.provinces) > 0 {
		parts = append(parts, fmt.Sprintf("ST=%v", s.provinces))
	}
	return parts
}

func (s certSubject) optionalAttributeFields() []string {
	var parts []string
	if len(s.countries) > 0 {
		parts = append(parts, fmt.Sprintf("countries: %v", s.countries))
	}
	if len(s.localities) > 0 {
		parts = append(parts, fmt.Sprintf("localities: %v", s.localities))
	}
	if len(s.provinces) > 0 {
		parts = append(parts, fmt.Sprintf("provinces: %v", s.provinces))
	}
	return parts
}
