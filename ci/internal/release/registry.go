package release

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Transport is the low-level OCI interface. The default implementation talks to
// a real registry via go-containerregistry; tests can substitute a fake.
type Transport interface {
	// CopyWithTags copies srcRef to dstRepo under each of the given tags.
	CopyWithTags(srcRef string, dstRepo string, tags []string) error
}

// RegistryClient performs release operations against an OCI registry.
type RegistryClient struct {
	host      string
	insecure  bool
	transport Transport
}

// Option configures a RegistryClient.
type Option func(*RegistryClient)

// WithTransport overrides the default GCR transport. Intended for testing.
func WithTransport(t Transport) Option {
	return func(c *RegistryClient) {
		c.transport = t
	}
}

// NewRegistryClient returns a RegistryClient for the given registry base URL.
// By default it uses the real GCR transport authenticated via DefaultKeychain.
// Pass WithTransport to override, e.g. in tests.
func NewRegistryClient(baseURL string, opts ...Option) *RegistryClient {
	insecure := strings.HasPrefix(baseURL, "http://")
	host := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	c := &RegistryClient{host: host, insecure: insecure}
	for _, o := range opts {
		o(c)
	}
	if c.transport == nil {
		c.transport = &gcrTransport{host: host, insecure: insecure}
	}
	return c
}

// Promote copies the source image to promoted-{commit}-{version} and promoted-latest.
func (c *RegistryClient) Promote(inputs PromoteInputs) ([]string, error) {
	return promote(inputs, c.transport)
}

// gcrTransport implements Transport via google/go-containerregistry.
type gcrTransport struct {
	host     string
	insecure bool
}

func (t *gcrTransport) CopyWithTags(srcRef string, dstRepo string, tags []string) error {
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

func (t *gcrTransport) nameOpts() []name.Option {
	if t.insecure {
		return []name.Option{name.Insecure}
	}
	return nil
}
