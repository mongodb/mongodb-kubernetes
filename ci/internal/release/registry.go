package release

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// ErrTagNotFound is returned by Registry.Digest when the reference does not
// exist yet (as opposed to a real registry-access failure).
var ErrTagNotFound = errors.New("tag not found")

// Registry is the abstracted OCI interface. The default implementation talks to
// a real registry via go-containerregistry; tests can substitute a fake.
type Registry interface {
	// CopyWithTags copies srcRef to dstRepo under each of the given tags.
	CopyWithTags(srcRef string, dstRepo string, tags []string) error
	// ListTags returns all tags for the given image repository reference.
	ListTags(repo string) ([]string, error)
	// Digest returns the manifest digest for ref (a full "host/repo:tag"
	// reference), or ErrTagNotFound if it doesn't exist.
	Digest(ref string) (string, error)
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

	var img v1.Image
	var idx v1.ImageIndex
	if desc.MediaType.IsIndex() {
		idx, err = desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("get index %s: %w", srcRef, err)
		}
	} else {
		img, err = desc.Image()
		if err != nil {
			return fmt.Errorf("get image %s: %w", srcRef, err)
		}
	}

	for _, tag := range tags {
		dst, err := name.NewTag(fmt.Sprintf("%s/%s:%s", t.host, dstRepo, tag), t.nameOpts()...)
		if err != nil {
			return fmt.Errorf("parse target tag %s: %w", tag, err)
		}
		if idx != nil {
			if err := remote.WriteIndex(dst, idx, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
				return fmt.Errorf("write index %s: %w", tag, err)
			}
		} else {
			if err := remote.Write(dst, img, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
				return fmt.Errorf("write image %s: %w", tag, err)
			}
		}
	}
	return nil
}

func (t *cRegistry) ListTags(repo string) ([]string, error) {
	// repo always arrives as a full reference (host/path); it may live on a
	// different host than the registry's own (e.g. listing an ECR staging repo
	// via a registry connected for the quay.io production host), so it must be
	// parsed as-is rather than reassembled under t.host.
	repoPath := strings.TrimPrefix(repo, "https://")
	repoPath = strings.TrimPrefix(repoPath, "http://")

	r, err := name.NewRepository(repoPath, t.nameOpts()...)
	if err != nil {
		return nil, fmt.Errorf("parse repo %s: %w", repo, err)
	}
	tags, err := remote.List(r, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, fmt.Errorf("list tags %s: %w", r, err)
	}
	return tags, nil
}

func (t *cRegistry) Digest(ref string) (string, error) {
	r, err := name.ParseReference(ref, t.nameOpts()...)
	if err != nil {
		return "", fmt.Errorf("parse ref %s: %w", ref, err)
	}
	desc, err := remote.Get(r, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return "", ErrTagNotFound
		}
		return "", fmt.Errorf("get %s: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

func (t *cRegistry) nameOpts() []name.Option {
	if t.insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}
