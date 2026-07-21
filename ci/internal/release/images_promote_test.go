package release

import (
	"errors"
	"strings"
	"testing"
)

type copyCall struct {
	srcRef  string
	dstRepo string
	tags    []string
}

type fakeRegistry struct {
	copies []copyCall
	fail   map[string]error
}

func (f *fakeRegistry) CopyWithTags(srcRef, dstRepo string, tags []string) error {
	if err := f.fail[srcRef]; err != nil {
		return err
	}
	f.copies = append(f.copies, copyCall{srcRef: srcRef, dstRepo: dstRepo, tags: tags})
	return nil
}

func TestPromoteImages(t *testing.T) {
	fake := &fakeRegistry{}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: "quay.io/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	results, err := PromoteImages(images, "abc1234", false, connect)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: got %d, want 2", len(results))
	}

	want := []copyCall{
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes:abc1234",
			dstRepo: "staging/mongodb-kubernetes",
			tags:    []string{PromotedTagFor("abc1234", "1.9.2"), promotedLatestTag()},
		},
		{
			srcRef:  "quay.io/staging/mongodb-kubernetes-readinessprobe:abc1234",
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

func TestPromoteImagesHardFailsOnMissingSource(t *testing.T) {
	fake := &fakeRegistry{
		fail: map[string]error{
			"quay.io/staging/mongodb-kubernetes-readinessprobe:abc1234": errors.New("source not found"),
		},
	}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: "quay.io/staging/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	_, err := PromoteImages(images, "abc1234", false, connect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPromoteImagesUsesShortCommitAsSourceTag(t *testing.T) {
	fake := &fakeRegistry{}
	connect := func(url string) Registry { return fake }

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: "quay.io/staging/mongodb-kubernetes", Version: "2.0.0", IsAnchor: true},
	}
	fullCommit := "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"

	results, err := PromoteImages(images, fullCommit, false, connect)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}

	// Source tag must be the first 8 chars of the full commit.
	wantSrcRef := "quay.io/staging/mongodb-kubernetes:a1b2c3d4"
	if len(fake.copies) != 1 {
		t.Fatalf("copies: got %d, want 1", len(fake.copies))
	}
	if fake.copies[0].srcRef != wantSrcRef {
		t.Errorf("srcRef: got %q, want %q", fake.copies[0].srcRef, wantSrcRef)
	}
	// Destination tag must still use the FULL commit.
	wantDstTag := PromotedTagFor(fullCommit, "2.0.0")
	if fake.copies[0].tags[0] != wantDstTag {
		t.Errorf("promoted tag: got %q, want %q", fake.copies[0].tags[0], wantDstTag)
	}
}

func TestPromoteImagesCommitRequired(t *testing.T) {
	connect := func(url string) Registry {
		t.Fatalf("connector must not be called when commit is missing")
		return nil
	}
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", Version: "1.9.2"}}
	_, err := PromoteImages(images, "", false, connect)
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
