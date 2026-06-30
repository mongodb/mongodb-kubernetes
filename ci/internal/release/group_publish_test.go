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

// insecureConnect is a RegistryConnector for tests, connecting over http.
func insecureConnect(host string) Registry {
	return DefaultRegistryConnector("http://" + host)
}

// assertTagDigest fails the test unless repo:tag exists and matches wantDigest.
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
		t.Errorf("%s:%s: digest got %q, want %q", repo, tag, desc.Digest, wantDigest)
	}
}

func TestPublishGroup(t *testing.T) {
	// Two separate registries stand in for the real ECR (staging) / quay.io
	// (production) split: PublishGroup must read promoted candidates from one
	// host and write production tags to a different one.
	stagingSrv := httptest.NewServer(registry.New())
	defer stagingSrv.Close()
	prodSrv := httptest.NewServer(registry.New())
	defer prodSrv.Close()

	stagingHost := strings.TrimPrefix(stagingSrv.URL, "http://")
	prodHost := strings.TrimPrefix(prodSrv.URL, "http://")

	opStaging := stagingHost + "/staging/mongodb-kubernetes"
	opProd := prodHost + "/mongodb/mongodb-kubernetes"
	rpStaging := stagingHost + "/staging/mongodb-kubernetes-readinessprobe"
	rpProd := prodHost + "/mongodb/mongodb-kubernetes-readinessprobe"

	opDigest := pushImage(t, fmt.Sprintf("%s:%s", opStaging, PromotedTagFor("abc1234", "1.9.2")), name.Insecure)
	rpDigest := pushImage(t, fmt.Sprintf("%s:%s", rpStaging, PromotedTagFor("abc1234", "1.0.24")), name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opStaging, ReleaseRepo: opProd, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: rpStaging, ReleaseRepo: rpProd, Version: "1.0.24"},
	}

	results, err := PublishGroup(images, "abc1234", false, insecureConnect)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results: got %d, want 2", len(results))
	}

	assertTagDigest(t, opProd, "1.9.2", opDigest)
	assertTagDigest(t, opProd, "latest", opDigest)
	assertTagDigest(t, rpProd, "1.0.24", rpDigest)
	assertTagDigest(t, rpProd, "latest", rpDigest)
}

func TestPublishGroupHardFailsOnMissingPromotedTag(t *testing.T) {
	stagingSrv := httptest.NewServer(registry.New())
	defer stagingSrv.Close()
	prodSrv := httptest.NewServer(registry.New())
	defer prodSrv.Close()

	stagingHost := strings.TrimPrefix(stagingSrv.URL, "http://")
	prodHost := strings.TrimPrefix(prodSrv.URL, "http://")

	opStaging := stagingHost + "/staging/mongodb-kubernetes"
	pushImage(t, fmt.Sprintf("%s:%s", opStaging, PromotedTagFor("abc1234", "1.9.2")), name.Insecure)
	// readiness-probe was never promoted to staging.

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opStaging, ReleaseRepo: prodHost + "/mongodb/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: stagingHost + "/staging/mongodb-kubernetes-readinessprobe", ReleaseRepo: prodHost + "/mongodb/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	_, err := PublishGroup(images, "abc1234", false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPublishGroupCommitRequired(t *testing.T) {
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", ReleaseRepo: "h/op"}}
	_, err := PublishGroup(images, "", false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit error, got %v", err)
	}
}
