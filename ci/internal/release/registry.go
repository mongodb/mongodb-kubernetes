package release

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Registry is the abstracted OCI interface. The default implementation talks to
// a real registry via go-containerregistry; tests can substitute a fake.
type Registry interface {
	// CopyWithTags copies srcRef to dstRepo under each of the given tags.
	CopyWithTags(srcRef string, dstRepo string, tags []string) error
}

// RegistryConnector builds a Registry for a registry base URL. The CLI passes
// DefaultRegistryConnector; tests inject one that returns a fake Registry.
type RegistryConnector func(url string) Registry

// DefaultRegistryConnector returns a Registry backed by the real GCR transport,
// authenticated via DefaultKeychain. It derives the registry host from url and
// treats an http:// scheme as insecure.
func DefaultRegistryConnector(url string) Registry {
	insecure := strings.HasPrefix(url, "http://")
	rest := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	host, _, _ := strings.Cut(rest, "/")
	return &cRegistry{host: host, insecure: insecure}
}

// cRegistry implements Registry via google/go-containerregistry.
type cRegistry struct {
	host     string
	insecure bool
}

func (t *cRegistry) CopyWithTags(srcRef string, dstRepo string, tags []string) error {
	src, err := name.ParseReference(srcRef, t.nameOpts()...)
	if err != nil {
		return fmt.Errorf("parse source ref %s: %w", srcRef, err)
	}
	desc, err := remote.Get(src, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return fmt.Errorf("get %s: %w", srcRef, err)
	}
	for _, tag := range tags {
		dst, err := name.NewTag(fmt.Sprintf("%s/%s:%s", t.host, dstRepo, tag), t.nameOpts()...)
		if err != nil {
			return fmt.Errorf("parse target tag %s: %w", tag, err)
		}
		if err := remote.Tag(dst, desc, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
			return fmt.Errorf("tag %s: %w", tag, err)
		}
	}
	return nil
}

func (t *cRegistry) nameOpts() []name.Option {
	if t.insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}
