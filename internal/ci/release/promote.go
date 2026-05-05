package release

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Promoter copies a source image to one or more tags in the target registry.
type Promoter interface {
	CopyWithTags(srcRef string, tags []string) error
}

// PromoteInputs are the parameters for a promote operation.
type PromoteInputs struct {
	Image   string // source image reference (required)
	Commit  string // commit SHA for the promoted tag name (required)
	Version string // version for the promoted tag name (required)
}

// Promote copies the source image to promoted-{commit}-{version} and promoted-latest,
// and returns the list of tags that were applied.
func Promote(inputs PromoteInputs, promoter Promoter) ([]string, error) {
	if inputs.Image == "" {
		return nil, errors.New("image is required")
	}
	if inputs.Commit == "" {
		return nil, errors.New("commit is required")
	}
	if inputs.Version == "" {
		return nil, errors.New("version is required")
	}

	tags := []string{
		PromotedTagFor(inputs.Commit, inputs.Version),
		promotedLatestTag(),
	}
	if err := promoter.CopyWithTags(inputs.Image, tags); err != nil {
		return nil, fmt.Errorf("promote %s: %w", inputs.Image, err)
	}
	return tags, nil
}

// gcrPromoter implements Promoter via google/go-containerregistry.
type gcrPromoter struct {
	host     string
	repo     string
	insecure bool
}

// NewOCIPromoter returns a Promoter that copies images into the given registry+repo.
func NewOCIPromoter(baseURL, repo string) Promoter {
	insecure := strings.HasPrefix(baseURL, "http://")
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	return &gcrPromoter{host: host, repo: repo, insecure: insecure}
}

func (p *gcrPromoter) CopyWithTags(srcRef string, tags []string) error {
	src, err := name.ParseReference(srcRef, p.nameOpts()...)
	if err != nil {
		return fmt.Errorf("parse source ref %s: %w", srcRef, err)
	}

	desc, err := remote.Get(src, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return fmt.Errorf("get %s: %w", srcRef, err)
	}

	for _, tag := range tags {
		dst, err := name.NewTag(fmt.Sprintf("%s/%s:%s", p.host, p.repo, tag), p.nameOpts()...)
		if err != nil {
			return fmt.Errorf("parse target tag %s: %w", tag, err)
		}
		if err := remote.Tag(dst, desc, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
			return fmt.Errorf("tag %s: %w", tag, err)
		}
	}
	return nil
}

func (p *gcrPromoter) nameOpts() []name.Option {
	if p.insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}
