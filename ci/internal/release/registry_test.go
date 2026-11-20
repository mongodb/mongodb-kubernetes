package release

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

func TestCRegistry_CopyWithTags(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	u, err := url.Parse(s.URL)
	require.NoError(t, err, "failed to parse test server url")
	host := u.Host // e.g., 127.0.0.1:54321

	fakeImg, err := random.Image(1024, 2)
	require.NoError(t, err, "failed to create random fake image")

	wantDigest, err := fakeImg.Digest()
	require.NoError(t, err, "failed to get src digest")

	srcRefStr := host + "/source-repo:v1"
	srcRef, err := name.ParseReference(srcRefStr, name.Insecure)
	require.NoError(t, err, "failed to parse src ref")
	require.NoError(t, remote.Write(srcRef, fakeImg), "failed to write source image")

	// 4. Exercise cRegistry.CopyWithTags (Insecure mode since HTTP)
	reg := &cRegistry{host: host, insecure: true}
	tags := []string{"latest", "v1.0.0"}

	require.NoError(t, reg.CopyWithTags(host+"/source-repo:v1", "target-repo", tags),
		"CopyWithTags failed")

	for _, tag := range tags {
		dstRef, err := name.ParseReference(host+"/target-repo:"+tag, name.Insecure)
		require.NoError(t, err, "failed to parse dst ref")

		dstDesc, err := remote.Get(dstRef)
		require.NoError(t, err, "failed to get target image %s", tag)

		// Verify SHA digest preservation!
		assert.Equal(t, dstDesc.Digest, wantDigest, "digest mismatch for tag %s", tag)
	}
}

func TestCRegistry_CopySignatures_CopiesSignatureIfPresent(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	u, err := url.Parse(s.URL)
	require.NoError(t, err, "failed to parse test server url")
	host := u.Host

	fakeImg, err := random.Image(1024, 2)
	require.NoError(t, err, "failed to create random fake image")

	imgDigest, err := fakeImg.Digest()
	require.NoError(t, err, "failed to get src digest")

	srcRefStr := host + "/source-repo:v1"
	srcRef, err := name.ParseReference(srcRefStr, name.Insecure)
	require.NoError(t, err, "failed to parse src ref")
	require.NoError(t, remote.Write(srcRef, fakeImg), "failed to write source image")

	// Simulate a cosign signature: a sibling tag in the SAME repo, named
	// after the image's own digest.
	sigTag := signatureTagFor(imgDigest)
	fakeSig, err := random.Image(512, 1)
	require.NoError(t, err, "failed to create fake signature image")
	wantSigDigest, err := fakeSig.Digest()
	require.NoError(t, err, "failed to get signature digest")

	sigRef, err := name.ParseReference(host+"/source-repo:"+sigTag, name.Insecure)
	require.NoError(t, err, "failed to parse signature ref")
	require.NoError(t, remote.Write(sigRef, fakeSig), "failed to write signature image")

	reg := &cRegistry{host: host, insecure: true}

	// Copy manifest first so the destination repo exists for the signature.
	require.NoError(t, reg.CopyWithTags(host+"/source-repo:v1", "target-repo", []string{"latest", "v1.0.0"}),
		"CopyWithTags failed")

	require.NoError(t, reg.CopySignatures(host+"/source-repo:v1", "target-repo", false),
		"CopySignatures failed")

	// The signature tag name is derived from the (unchanged) image digest,
	// so a single copy covers every applied tag.
	dstSigRef, err := name.ParseReference(host+"/target-repo:"+sigTag, name.Insecure)
	require.NoError(t, err, "failed to parse dst signature ref")

	dstSigDesc, err := remote.Get(dstSigRef)
	require.NoError(t, err, "signature was not copied to target repo")
	assert.Equal(t, wantSigDigest, dstSigDesc.Digest, "signature digest mismatch")
}

func TestCRegistry_CopySignatures_CopiesSignaturesForIndexAndChildManifests(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	u, err := url.Parse(s.URL)
	require.NoError(t, err, "failed to parse test server url")
	host := u.Host

	// Build a fake multiarch index with 2 child manifests.
	idx, err := random.Index(1024, 2, 2)
	require.NoError(t, err, "failed to create random index")

	idxDigest, err := idx.Digest()
	require.NoError(t, err, "failed to get index digest")

	// Write index to source repo under v1 tag.
	srcRefStr := host + "/source-repo:v1"
	srcRef, err := name.ParseReference(srcRefStr, name.Insecure)
	require.NoError(t, err, "failed to parse src ref")
	require.NoError(t, remote.WriteIndex(srcRef, idx), "failed to write source index")

	// Get child manifest digests.
	idxManifest, err := idx.IndexManifest()
	require.NoError(t, err, "failed to get index manifest")
	require.Len(t, idxManifest.Manifests, 2, "expected 2 child manifests")

	// Write signature images for index digest and each child digest.
	wantSigDigests := make(map[string]string) // sigTag -> wantSigDigest

	allDigests := []v1.Hash{idxDigest}
	for _, m := range idxManifest.Manifests {
		allDigests = append(allDigests, m.Digest)
	}
	for _, d := range allDigests {
		sigTag := signatureTagFor(d)
		fakeSig, err := random.Image(512, 1)
		require.NoError(t, err, "failed to create fake signature image")
		sigDigest, err := fakeSig.Digest()
		require.NoError(t, err, "failed to get signature digest")
		wantSigDigests[sigTag] = sigDigest.String()

		sigRef, err := name.ParseReference(host+"/source-repo:"+sigTag, name.Insecure)
		require.NoError(t, err, "failed to parse signature ref")
		require.NoError(t, remote.Write(sigRef, fakeSig), "failed to write signature image")
	}

	reg := &cRegistry{host: host, insecure: true}

	// Copy manifest first so the destination repo exists for the signature.
	require.NoError(t, reg.CopyWithTags(host+"/source-repo:v1", "target-repo", []string{"latest"}),
		"CopyWithTags failed")

	require.NoError(t, reg.CopySignatures(host+"/source-repo:v1", "target-repo", false),
		"CopySignatures failed")

	// Assert that the signature for the index digest AND each child digest
	// were copied to the destination.
	for sigTag, wantDigest := range wantSigDigests {
		dstSigRef, err := name.ParseReference(host+"/target-repo:"+sigTag, name.Insecure)
		require.NoError(t, err, "failed to parse dst signature ref for %s", sigTag)

		dstSigDesc, err := remote.Get(dstSigRef)
		require.NoError(t, err, "signature %s was not copied to target repo", sigTag)
		assert.Equal(t, wantDigest, dstSigDesc.Digest.String(), "signature digest mismatch for %s", sigTag)
	}
}

func TestCRegistry_CopySignatures_NoSignatureIsNotAnError(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	u, err := url.Parse(s.URL)
	require.NoError(t, err, "failed to parse test server url")
	host := u.Host

	fakeImg, err := random.Image(1024, 2)
	require.NoError(t, err, "failed to create random fake image")

	imgDigest, err := fakeImg.Digest()
	require.NoError(t, err, "failed to get src digest")

	srcRefStr := host + "/unsigned-repo:v1"
	srcRef, err := name.ParseReference(srcRefStr, name.Insecure)
	require.NoError(t, err, "failed to parse src ref")
	require.NoError(t, remote.Write(srcRef, fakeImg), "failed to write source image")

	reg := &cRegistry{host: host, insecure: true}

	require.NoError(t, reg.CopyWithTags(host+"/unsigned-repo:v1", "target-repo-unsigned", []string{"latest"}),
		"CopyWithTags should succeed even when no signature exists")

	err = reg.CopySignatures(host+"/unsigned-repo:v1", "target-repo-unsigned", false)
	require.Error(t, err, "CopySignatures should return error when no signature exists")
	require.True(t, errors.Is(err, ErrSignatureNotFound),
		"CopySignatures should wrap ErrSignatureNotFound for missing top-level signature")

	// Confirm no signature tag was created at the destination either.
	sigTag := signatureTagFor(imgDigest)
	dstSigRef, err := name.ParseReference(host+"/target-repo-unsigned:"+sigTag, name.Insecure)
	require.NoError(t, err, "failed to parse dst signature ref")
	_, err = remote.Get(dstSigRef)
	assert.Error(t, err, "expected no signature tag to exist at destination")
}
