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

	results, err := PublishGroup(images, "abc1234", false, false, insecureConnect)
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

	_, err := PublishGroup(images, "abc1234", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPublishGroupCommitRequired(t *testing.T) {
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", ReleaseRepo: "h/op"}}
	_, err := PublishGroup(images, "", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit error, got %v", err)
	}
}

func TestPublishGroupRefusesAllOnAnyConflict(t *testing.T) {
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

	pushImage(t, fmt.Sprintf("%s:%s", opStaging, PromotedTagFor("abc1234", "1.9.2")), name.Insecure)
	pushImage(t, fmt.Sprintf("%s:%s", rpStaging, PromotedTagFor("abc1234", "1.0.24")), name.Insecure)

	// Production already has a DIFFERENT image at readiness-probe's :1.0.24 tag: a stomp.
	pushImage(t, rpProd+":1.0.24", name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opStaging, ReleaseRepo: opProd, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: rpStaging, ReleaseRepo: rpProd, Version: "1.0.24"},
	}

	_, err := PublishGroup(images, "abc1234", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected conflict error mentioning readiness-probe, got %v", err)
	}

	// All-or-nothing: operator's production tag must NOT have been written.
	if _, err := remote.Get(mustTagRef(t, opProd, "1.9.2")); err == nil {
		t.Error("operator production tag should not exist: group must be refused before any writes")
	}

	// With --force, the whole group proceeds and overwrites the conflicting tag.
	results, err := PublishGroup(images, "abc1234", true, false, insecureConnect)
	if err != nil {
		t.Fatalf("force: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("force: results: got %d, want 2", len(results))
	}
}
