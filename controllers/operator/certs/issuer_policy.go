package certs

import (
	"fmt"
	"strings"

	certmanagerv1 "github.com/cert-manager/cert-manager/pkg/apis/certmanager/v1"
	"golang.org/x/xerrors"

	mdbv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/mdb"
)

// AllowedIssuers is the type that stores the parsed issuers that are configured to be used for
// managedCertificate feature. err stores any error that were encountered while parsing the
// user configured values.
type AllowedIssuers struct {
	issuers []AllowedIssuer
	err     string
}

type AllowedIssuer struct {
	Kind string
	Name string
}

func (i AllowedIssuer) String() string { return i.Kind + "/" + i.Name }

// ParseAllowedIssuers reads a comma-separated list of "Kind/name" entries, where an entry without
// a kind means Issuer. A value that cannot be read is recorded as invalid and that means no issuer is allowed.
// If the issuers are not configured by users (`raw` is empty), it would mean there are no restrictions
// and any issuer can be used by managed certificate feature.
func ParseAllowedIssuers(raw string) AllowedIssuers {
	if strings.TrimSpace(raw) == "" {
		return AllowedIssuers{}
	}

	entries := make([]AllowedIssuer, 0)
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// if `/` is not in the entry it means the kind is issuer
		kind, name := certmanagerv1.IssuerKind, entry

		// if `/` is found in entry, first part is kind and second one is name
		if k, n, found := strings.Cut(entry, "/"); found {
			kind, name = k, n
		}
		if kind != certmanagerv1.IssuerKind && kind != certmanagerv1.ClusterIssuerKind {
			return AllowedIssuers{err: fmt.Sprintf("issuer kind %q is not Issuer or ClusterIssuer", kind)}
		}
		if name == "" {
			return AllowedIssuers{err: fmt.Sprintf("issuer entry %q has no name", entry)}
		}

		entries = append(entries, AllowedIssuer{Kind: kind, Name: name})
	}
	return AllowedIssuers{issuers: entries}
}

// IsUnrestricted reports that the operator was not configured with an issuer list (`security.managedCertificate.allowedIssuers`),
// so any issuer may be used.
func (a AllowedIssuers) IsUnrestricted() bool { return len(a.issuers) == 0 && a.err == "" }

// Allows reports whether an issuerRef naming kind and name may be used
func (a AllowedIssuers) Allows(kind, name string) bool {
	if a.IsUnrestricted() {
		return true
	}
	if kind == "" {
		kind = certmanagerv1.IssuerKind
	}
	for _, issuer := range a.issuers {
		if issuer.Kind == kind && issuer.Name == name {
			return true
		}
	}
	return false
}

// String renders the allowed issuers the way they are configured, for failure messages.
// This is how allowed issuers are set as env var in operator pod.
func (a AllowedIssuers) String() string {
	names := make([]string, 0, len(a.issuers))
	for _, issuer := range a.issuers {
		names = append(names, issuer.String())
	}
	return strings.Join(names, ", ")
}

// ValidateIssuerRef checks one configured issuerRef against the operator's allowed issuers.
// fieldPath names the issuerRef in the resource, for the message. An unset issuerRef is fine: the
// operator then signs the certificate with its own CA.
func (a AllowedIssuers) ValidateIssuerRef(ref *mdbv1.IssuerRef, fieldPath string) error {
	if ref == nil || ref.Name == "" {
		return nil
	}
	if a.err != "" {
		return xerrors.Errorf("the operator's allowed-issuer list could not be read (%s), so the issuerRef %s cannot be used", a.err, fieldPath)
	}
	if a.Allows(ref.Kind, ref.Name) {
		return nil
	}
	kind := ref.Kind
	if kind == "" {
		kind = certmanagerv1.IssuerKind
	}
	return xerrors.Errorf("the issuerRef %s points at %s, but this operator is configured to allow only %s.",
		fieldPath, AllowedIssuer{Kind: kind, Name: ref.Name}, a)
}

// ValidateManagedCertificateIssuers checks the issuerRef(s) configured under
// security.managedCertificate for a resource against the operator's allowed issuers. pathPrefix is prepended to
// the field names in the message, so an AppDB can be reported under its own spec path.
func (r CertManagerReadiness) ValidateManagedCertificateIssuers(sec *mdbv1.Security, pathPrefix string) error {
	if sec == nil || sec.ManagedCertificate == nil {
		return nil
	}
	for _, cat := range []struct {
		category certCategory
		cfg      *mdbv1.CertificateConfig
	}{
		{categoryServer, sec.ManagedCertificate.Server},
		{categoryClient, sec.ManagedCertificate.Client},
	} {
		if err := r.ValidateManagedCertificateConfig(cat.cfg, fmt.Sprintf("%ssecurity.managedCertificate.%s.issuerRef", pathPrefix, cat.category)); err != nil {
			return err
		}
	}
	return nil
}

// ValidateManagedCertificateConfig checks the issuerRef of one managedCertificate block (client/server). The Ops
// Manager resource configures one block per component (om, appdb, backup) instead of the
// server/client pair.
func (r CertManagerReadiness) ValidateManagedCertificateConfig(cfg *mdbv1.CertificateConfig, fieldPath string) error {
	if cfg == nil {
		return nil
	}
	return r.AllowedIssuers.ValidateIssuerRef(cfg.IssuerRef, fieldPath)
}
