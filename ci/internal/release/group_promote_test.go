package release

import (
	"errors"
	"strings"
	"testing"
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

func (f *fakeRegistry) GetDigest(ref string) (string, error) {
	return "", errors.New("fakeRegistry.GetDigest not implemented")
}

func TestPromoteGroup(t *testing.T) {
	fake := &fakeRegistry{}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: "quay.io/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	results, err := PromoteGroup(images, "abc1234", false, connect)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: got %d, want 2", len(results))
	}

	// Each image must be copied from its version-tagged source in its primary
	// staging repo to both promoted tags, with host split off from the repo path.
	want := []copyCall{
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes:1.9.2",
			dstRepo: "staging/mongodb-kubernetes",
			tags:    []string{PromotedTagFor("abc1234", "1.9.2"), promotedLatestTag()},
		},
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes-readinessprobe:1.0.24",
			dstRepo: "staging/mongodb-kubernetes-readinessprobe",
			tags:    []string{PromotedTagFor("abc1234", "1.0.24"), promotedLatestTag()},
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

	_, err := PromoteGroup(images, "abc1234", false, connect)
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
	_, err := PromoteGroup(images, "", false, connect)
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
