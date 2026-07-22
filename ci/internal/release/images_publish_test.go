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

func TestPublishImages(t *testing.T) {
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

	results, err := PublishImages(images, "abc1234", "latest", false, false, insecureConnect)
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

func TestPublishImagesHardFailsOnMissingPromotedTag(t *testing.T) {
	stagingSrv := httptest.NewServer(registry.New())
	defer stagingSrv.Close()
	prodSrv := httptest.NewServer(registry.New())
	defer prodSrv.Close()

	stagingHost := strings.TrimPrefix(stagingSrv.URL, "http://")
	prodHost := strings.TrimPrefix(prodSrv.URL, "http://")

	opStaging := stagingHost + "/staging/mongodb-kubernetes"
	pushImage(t, fmt.Sprintf("%s:%s", opStaging, PromotedTagFor("abc1234", "1.9.2")), name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opStaging, ReleaseRepo: prodHost + "/mongodb/mongodb-kubernetes", Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: stagingHost + "/staging/mongodb-kubernetes-readinessprobe", ReleaseRepo: prodHost + "/mongodb/mongodb-kubernetes-readinessprobe", Version: "1.0.24"},
	}

	_, err := PublishImages(images, "abc1234", "latest", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected hard failure mentioning readiness-probe, got %v", err)
	}
}

func TestPublishImagesCommitRequired(t *testing.T) {
	images := []ReleaseImage{{Name: "operator", StagingRepo: "h/staging/op", ReleaseRepo: "h/op"}}
	_, err := PublishImages(images, "", "", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "commit") {
		t.Fatalf("expected commit error, got %v", err)
	}
}

func TestPublishImagesRefusesAllOnAnyConflict(t *testing.T) {
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

	pushImage(t, rpProd+":1.0.24", name.Insecure)

	images := []ReleaseImage{
		{Name: "operator", StagingRepo: opStaging, ReleaseRepo: opProd, Version: "1.9.2", IsAnchor: true},
		{Name: "readiness-probe", StagingRepo: rpStaging, ReleaseRepo: rpProd, Version: "1.0.24"},
	}

	_, err := PublishImages(images, "abc1234", "latest", false, false, insecureConnect)
	if err == nil || !strings.Contains(err.Error(), "readiness-probe") {
		t.Fatalf("expected conflict error mentioning readiness-probe, got %v", err)
	}

	if _, err := remote.Get(mustTagRef(t, opProd, "1.9.2")); err == nil {
		t.Error("operator production tag should not exist: images must be refused before any writes")
	}

	results, err := PublishImages(images, "abc1234", "latest", true, false, insecureConnect)
	if err != nil {
		t.Fatalf("force: unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("force: results: got %d, want 2", len(results))
	}
}
