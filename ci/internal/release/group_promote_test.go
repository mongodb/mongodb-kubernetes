package release

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestPromoteGroup(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	// Two group members, each already pushed to its PRIMARY staging repo under
	// the version tag, mirroring what the staging build leaves behind.
	opRepo := host + "/staging/mongodb-kubernetes"
	rpRepo := host + "/staging/mongodb-kubernetes-readinessprobe"
	opDigest := pushImage(t, opRepo+":1.9.2", name.Insecure)
	rpDigest := pushImage(t, rpRepo+":1.0.24", name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opRepo, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: rpRepo, Version: "1.0.24"},
	}

	results, err := PromoteGroup(images, "abc1234", false, insecureClientFor)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: got %d, want 2", len(results))
	}

	// Each image's primary repo must now carry both promoted tags pointing at
	// the same digest as the version-tagged source.
	assertTagDigest(t, opRepo, PromotedTagFor("abc1234", "1.9.2"), opDigest)
	assertTagDigest(t, opRepo, promotedLatestTag(), opDigest)
	assertTagDigest(t, rpRepo, PromotedTagFor("abc1234", "1.0.24"), rpDigest)
	assertTagDigest(t, rpRepo, promotedLatestTag(), rpDigest)
}

func TestPromoteGroupHardFailsOnMissingSource(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	opRepo := host + "/staging/mongodb-kubernetes"
	pushImage(t, opRepo+":1.9.2", name.Insecure)
	// readiness-probe source is intentionally NOT pushed.

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opRepo, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: host + "/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	_, err := PromoteGroup(images, "abc1234", false, insecureClientFor)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPromoteGroupCommitRequired(t *testing.T) {
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", Version: "1.9.2"}}
	_, err := PromoteGroup(images, "", false, insecureClientFor)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit error, got %v", err)
	}
}

func TestSplitHostRepo(t *testing.T) {
	host, path := splitHostRepo("quay.io/mongodb/staging/x")
	if host != "quay.io" || path != "mongodb/staging/x" {
		t.Errorf("got (%q, %q)", host, path)
	}
	if h, p := splitHostRepo("norepo"); h != "" || p != "norepo" {
		t.Errorf("no-slash: got (%q, %q)", h, p)
	}
}

func insecureClientFor(host string) *RegistryClient {
	return NewRegistryClient("http://" + host)
}

func assertTagDigest(t *testing.T, repo, tag, wantDigest string) {
	t.Helper()
	ref, err := name.NewTag(fmt.Sprintf("%s:%s", repo, tag), name.Insecure)
	if err != nil {
		t.Fatalf("parse tag %s:%s: %v", repo, tag, err)
	}
	desc, err := remote.Get(ref)
	if err != nil {
		t.Fatalf("tag %s:%s not found: %v", repo, tag, err)
	}
	if desc.Digest.String() != wantDigest {
		t.Errorf("%s:%s digest got %q, want %q", repo, tag, desc.Digest, wantDigest)
	}
}
