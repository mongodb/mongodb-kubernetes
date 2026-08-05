package release

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestCRegistry_CopyWithTags_CopiesSignatureIfPresent(t *testing.T) {
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
	tags := []string{"latest", "v1.0.0"}

	require.NoError(t, reg.CopyWithTags(host+"/source-repo:v1", "target-repo", tags),
		"CopyWithTags failed")

	// The signature tag name is derived from the (unchanged) image digest,
	// so a single copy covers every applied tag.
	dstSigRef, err := name.ParseReference(host+"/target-repo:"+sigTag, name.Insecure)
	require.NoError(t, err, "failed to parse dst signature ref")

	dstSigDesc, err := remote.Get(dstSigRef)
	require.NoError(t, err, "signature was not copied to target repo")
	assert.Equal(t, wantSigDigest, dstSigDesc.Digest, "signature digest mismatch")
}

func TestCRegistry_CopyWithTags_NoSignatureIsNotAnError(t *testing.T) {
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

	// Confirm no signature tag was created at the destination either.
	sigTag := signatureTagFor(imgDigest)
	dstSigRef, err := name.ParseReference(host+"/target-repo-unsigned:"+sigTag, name.Insecure)
	require.NoError(t, err, "failed to parse dst signature ref")
	_, err = remote.Get(dstSigRef)
	assert.Error(t, err, "expected no signature tag to exist at destination")
}
