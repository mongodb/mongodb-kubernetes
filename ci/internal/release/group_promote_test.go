package release

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// copyCall records one CopyWithTags invocation on the fake registry.
type copyCall struct {
	srcRef  string
	dstRepo string
	tags    []string
}

// fakeRegistry is an in-memory Registry: it records every copy and can be told
// to fail for a specific source ref, so the group logic can be exercised
// without standing up a real registry.
type fakeRegistry struct {
	copies []copyCall
	fail   map[string]error // srcRef -> error to return
}

func (f *fakeRegistry) CopyWithTags(srcRef, dstRepo string, tags []string) error {
	if err := f.fail[srcRef]; err != nil {
		return err
	}
	f.copies = append(f.copies, copyCall{srcRef: srcRef, dstRepo: dstRepo, tags: tags})
	return nil
}

func (f *fakeRegistry) ListTags(repo string) ([]string, error) {
	return nil, errors.New("fakeRegistry.ListTags not implemented")
}

// Digest returns a synthetic digest for any ref not marked to fail, except a
// promoted-tag ref (which stands in for a destination tag that never got
// written in this fake, so a stomp check against it always finds it absent).
func (f *fakeRegistry) Digest(ref string) (string, error) {
	if err := f.fail[ref]; err != nil {
		return "", err
	}
	if strings.Contains(ref, PromotedTagPrefix) {
		return "", ErrTagNotFound
	}
	return "digest:" + ref, nil
}

func TestPromoteGroup(t *testing.T) {
	fake := &fakeRegistry{}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: "quay.io/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	results, err := PromoteGroup(images, "abc1234", false, false, connect)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: got %d, want 2", len(results))
	}

	// Each image must be copied from its version-tagged source in its primary
	// staging repo to both promoted tags (immutable version tag first, then
	// the mutable latest pointer), with host split off from the repo path.
	want := []copyCall{
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes:1.9.2",
			dstRepo: "staging/mongodb-kubernetes",
			tags:    []string{PromotedTagFor("abc1234", "1.9.2")},
		},
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes:1.9.2",
			dstRepo: "staging/mongodb-kubernetes",
			tags:    []string{promotedLatestTag()},
		},
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes-readinessprobe:1.0.24",
			dstRepo: "staging/mongodb-kubernetes-readinessprobe",
			tags:    []string{PromotedTagFor("abc1234", "1.0.24")},
		},
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes-readinessprobe:1.0.24",
			dstRepo: "staging/mongodb-kubernetes-readinessprobe",
			tags:    []string{promotedLatestTag()},
		},
	}
	if len(fake.copies) != len(want) {
		t.Fatalf("copies: got %d, want %d", len(fake.copies), len(want))
	}
	for i, w := range want {
		got := fake.copies[i]
		if got.srcRef != w.srcRef || got.dstRepo != w.dstRepo || strings.Join(got.tags, ",") != strings.Join(w.tags, ",") {
			t.Errorf("copy %d: got %+v, want %+v", i, got, w)
		}
	}
}

func TestPromoteGroupHardFailsOnMissingSource(t *testing.T) {
	fake := &fakeRegistry{
		fail: map[string]error{
			"quay.io/staging/mongodb-kubernetes-readinessprobe:1.0.24": errors.New("source not found"),
		},
	}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: "quay.io/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	_, err := PromoteGroup(images, "abc1234", false, false, connect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPromoteGroupCommitRequired(t *testing.T) {
	connect := func(url string) Registry {
		t.Fatalf("connector must not be called when commit is missing")
		return nil
	}
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", Version: "1.9.2"}}
	_, err := PromoteGroup(images, "", false, false, connect)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit error, got %v", err)
	}
}

func TestPromoteGroupRefusesAllOnAnyConflict(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	opRepo := host + "/staging/mongodb-kubernetes"
	rpRepo := host + "/staging/mongodb-kubernetes-readinessprobe"
	pushImage(t, opRepo+":1.9.2", name.Insecure)
	pushImage(t, rpRepo+":1.0.24", name.Insecure)

	// Pre-existing promoted tag for readiness-probe at a DIFFERENT digest than
	// what's about to be promoted: a stomp.
	pushImage(t, rpRepo+":"+PromotedTagFor("abc1234", "1.0.24"), name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opRepo, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: rpRepo, Version: "1.0.24"},
	}

	_, err := PromoteGroup(images, "abc1234", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected conflict error mentioning readiness-probe, got %v", err)
	}

	// All-or-nothing: the operator's promoted tag must NOT have been written
	// even though only readiness-probe conflicted.
	if _, err := remote.Get(mustTagRef(t, opRepo, PromotedTagFor("abc1234", "1.9.2"))); err == nil {
		t.Error("operator promoted tag should not exist: group must be refused before any writes")
	}

	// With --force, the whole group proceeds and overwrites the conflicting tag.
	results, err := PromoteGroup(images, "abc1234", true, false, insecureConnect)
	if err != nil {
		t.Fatalf("force: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("force: results: got %d, want 2", len(results))
	}
	opDigest := mustDigest(t, opRepo, "1.9.2")
	assertTagDigest(t, opRepo, PromotedTagFor("abc1234", "1.9.2"), opDigest)
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

// insecureConnect is a RegistryConnector for tests, connecting over http.
func insecureConnect(host string) Registry {
	return DefaultRegistryConnector("http://" + host)
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

func mustTagRef(t *testing.T, repo, tag string) name.Reference {
	t.Helper()
	ref, err := name.NewTag(fmt.Sprintf("%s:%s", repo, tag), name.Insecure)
	if err != nil {
		t.Fatalf("parse tag %s:%s: %v", repo, tag, err)
	}
	return ref
}

func mustDigest(t *testing.T, repo, tag string) string {
	t.Helper()
	desc, err := remote.Get(mustTagRef(t, repo, tag))
	if err != nil {
		t.Fatalf("get %s:%s: %v", repo, tag, err)
	}
	return desc.Digest.String()
}
