package vault

import (
	"path"
	"strings"

	"golang.org/x/xerrors"
)

// SecretPath joins basePath with the given CR-derived components into a Vault
// logical path. Each component must be a single path segment: names taken from
// custom resources are attacker-controlled, and a component such as
// "../victim-ns/victim-cert" would otherwise walk the built path out of the
// resource's own namespace prefix and into another tenant's secrets.
func SecretPath(basePath string, components ...string) (string, error) {
	segments := make([]string, 0, len(components)+1)
	segments = append(segments, basePath)
	for _, component := range components {
		if err := validateSecretPathComponent(component); err != nil {
			return "", err
		}
		segments = append(segments, component)
	}
	return strings.Join(segments, "/"), nil
}

func validateSecretPathComponent(component string) error {
	if component == "" {
		return xerrors.Errorf("invalid vault secret path component: must not be empty")
	}
	if component == "." || component == ".." {
		return xerrors.Errorf("invalid vault secret path component %q: must not be a relative path element", component)
	}
	if strings.ContainsAny(component, `/\`) {
		return xerrors.Errorf("invalid vault secret path component %q: must be a single path segment", component)
	}
	return nil
}

// validateSecretPath rejects logical paths that are not already in canonical
// form. A path containing "..", "." or empty segments cannot have been built
// from a trusted base path and namespace/name pair, so it must never reach
// Vault. This is the backstop for any sink that does not yet build its path
// through SecretPath.
func validateSecretPath(secretPath string) error {
	if secretPath == "" {
		return xerrors.Errorf("invalid vault secret path: must not be empty")
	}
	if strings.HasPrefix(secretPath, "/") {
		return xerrors.Errorf("invalid vault secret path %q: must not be absolute", secretPath)
	}
	if path.Clean(secretPath) != secretPath {
		return xerrors.Errorf("invalid vault secret path %q: must be in canonical form", secretPath)
	}
	return nil
}
