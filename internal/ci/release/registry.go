package release

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type gcrRegistry struct {
	host     string
	repo     string
	insecure bool
}

// NewOCIRegistry returns a Registry backed by google/go-containerregistry.
// baseURL may include a scheme (e.g. "https://quay.io" or "http://localhost:5000");
// the scheme is stripped and used to set insecure mode.
func NewOCIRegistry(baseURL, repo string) Registry {
	insecure := strings.HasPrefix(baseURL, "http://")
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	return &gcrRegistry{host: host, repo: repo, insecure: insecure}
}

func (r *gcrRegistry) ResolveByTag(tag string) (ImageInfo, error) {
	ref, err := r.ref(tag)
	if err != nil {
		return ImageInfo{}, err
	}
	desc, err := remote.Head(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return ImageInfo{}, fmt.Errorf("tag %s: %w", tag, err)
	}
	return ImageInfo{Tag: tag, Digest: desc.Digest.String()}, nil
}

func (r *gcrRegistry) FindTagsByDigest(digest string) ([]string, error) {
	repository, err := r.repository()
	if err != nil {
		return nil, err
	}
	tags, err := remote.List(repository, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return nil, fmt.Errorf("list tags for %s: %w", r.repo, err)
	}

	var matches []string
	for _, tag := range tags {
		ref, err := r.ref(tag)
		if err != nil {
			continue
		}
		desc, err := remote.Head(ref, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		if err != nil {
			continue
		}
		if desc.Digest.String() == digest {
			matches = append(matches, tag)
		}
	}
	return matches, nil
}

func (r *gcrRegistry) ref(tag string) (name.Reference, error) {
	return name.ParseReference(fmt.Sprintf("%s/%s:%s", r.host, r.repo, tag), r.nameOpts()...)
}

func (r *gcrRegistry) repository() (name.Repository, error) {
	return name.NewRepository(fmt.Sprintf("%s/%s", r.host, r.repo), r.nameOpts()...)
}

func (r *gcrRegistry) nameOpts() []name.Option {
	if r.insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}
