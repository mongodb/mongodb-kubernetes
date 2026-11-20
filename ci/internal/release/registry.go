package release

import (
	"errors"
	"fmt"
	"log"
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

// ErrSignatureNotFound is returned when a cosign .sig tag does not exist.
var ErrSignatureNotFound = errors.New("signature not found")

// Registry is the abstracted OCI interface. The default implementation talks to
// a real registry via go-containerregistry; tests can substitute a fake.
type Registry interface {
	// CopyWithTags copies srcRef to dstRepo under each of the given tags.
	CopyWithTags(srcRef string, dstRepo string, tags []string) error
	// CopySignatures copies the cosign signature for srcRef's digest (and,
	// if srcRef is a multiarch index, for each child manifest digest too —
	// returning ErrSignatureNotFound when the top-level .sig tag is missing,
	// or when a child manifest .sig tag is missing and
	// allowPartialSignatures is false).
	CopySignatures(srcRef string, dstRepo string, allowPartialSignatures bool) error
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

func signatureTagFor(digest v1.Hash) string {
	return strings.ReplaceAll(digest.String(), ":", "-") + ".sig"
}

func (t *cRegistry) CopyWithTags(srcRef string, dstRepo string, tags []string) error {
	src, err := name.ParseReference(srcRef, t.nameOpts()...)
	if err != nil {
		return fmt.Errorf("failed to parse source ref %s: %w", srcRef, err)
	}
	desc, err := remote.Get(src, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return fmt.Errorf("failed to get %s: %w", srcRef, err)
	}

	var img v1.Image
	var idx v1.ImageIndex
	if desc.MediaType.IsIndex() {
		idx, err = desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("failed to get index %s: %w", srcRef, err)
		}
	} else {
		img, err = desc.Image()
		if err != nil {
			return fmt.Errorf("failed to get image %s: %w", srcRef, err)
		}
	}

	for _, tag := range tags {
		dst, err := name.NewTag(fmt.Sprintf("%s/%s:%s", t.host, dstRepo, tag), t.nameOpts()...)
		if err != nil {
			return fmt.Errorf("failed to parse target tag %s: %w", tag, err)
		}
		if idx != nil {
			if err := remote.WriteIndex(dst, idx, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
				return fmt.Errorf("failed to write index %s: %w", tag, err)
			}
		} else {
			if err := remote.Write(dst, img, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
				return fmt.Errorf("failed to write image %s: %w", tag, err)
			}
		}
	}
	return nil
}

// CopySignatures copies the cosign signature for srcRef's digest (and, if
// srcRef is a multiarch index, for each child manifest digest too) from
// srcRef's repository to dstRepo.
func (t *cRegistry) CopySignatures(srcRef string, dstRepo string, allowPartialSignatures bool) error {
	src, err := name.ParseReference(srcRef, t.nameOpts()...)
	if err != nil {
		return fmt.Errorf("failed to parse source ref %s: %w", srcRef, err)
	}
	desc, err := remote.Get(src, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return fmt.Errorf("failed to get %s: %w", srcRef, err)
	}

	if err := t.copySignature(src, desc.Digest, dstRepo); err != nil {
		if allowPartialSignatures && errors.Is(err, ErrSignatureNotFound) {
			log.Printf("no signature found for %s, skipping copy", srcRef)
		} else {
			return fmt.Errorf("failed to copy signature for %s: %w", srcRef, err)
		}
	}

	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("failed to get index %s: %w", srcRef, err)
		}
		idxManifest, err := idx.IndexManifest()
		if err != nil {
			return fmt.Errorf("failed to read index manifest for %s: %w", srcRef, err)
		}
		for _, m := range idxManifest.Manifests {
			if err := t.copySignature(src, m.Digest, dstRepo); err != nil {
				if allowPartialSignatures && errors.Is(err, ErrSignatureNotFound) {
					log.Printf("no signature found for child manifest %s, skipping copy (%s)", m.Digest, srcRef)
					continue
				}
				return fmt.Errorf("failed to copy signature for %s (child %s): %w", srcRef, m.Digest, err)
			}
		}
	}

	return nil
}

// copySignature copies the cosign signature sibling tag
func (t *cRegistry) copySignature(src name.Reference, digest v1.Hash, dstRepo string) error {
	sigTag := signatureTagFor(digest)

	srcSigRef := src.Context().Tag(sigTag)
	sigDesc, err := remote.Get(srcSigRef, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return ErrSignatureNotFound
		}
		return fmt.Errorf("failed to get signature %s: %w", srcSigRef, err)
	}

	sigImg, err := sigDesc.Image()
	if err != nil {
		return fmt.Errorf("failed to read signature image %s: %w", srcSigRef, err)
	}

	dstSigRef, err := name.NewTag(fmt.Sprintf("%s/%s:%s", t.host, dstRepo, sigTag), t.nameOpts()...)
	if err != nil {
		return fmt.Errorf("failed to parse signature target tag %s: %w", sigTag, err)
	}
	if err := remote.Write(dstSigRef, sigImg, remote.WithAuthFromKeychain(authn.DefaultKeychain)); err != nil {
		return fmt.Errorf("failed to write signature %s: %w", sigTag, err)
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
