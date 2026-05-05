package release

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// pushImage pushes img to the test registry under each tag and returns the digest.
func pushImage(t *testing.T, registryHost, repo string, tags []string) string {
	t.Helper()
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	for _, tag := range tags {
		ref, err := name.ParseReference(fmt.Sprintf("%s/%s:%s", registryHost, repo, tag), name.Insecure)
		if err != nil {
			t.Fatalf("parse ref %s: %v", tag, err)
		}
		if err := remote.Write(ref, img); err != nil {
			t.Fatalf("push %s: %v", tag, err)
		}
	}
	return d.String()
}

func TestOCIRegistryClient_ResolveByTag(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/myimage"
	digest := pushImage(t, host, repo, []string{"promoted-abc1234-1.9.0"})

	client := NewOCIRegistry(srv.URL, repo)

	tests := []struct {
		name       string
		tag        string
		wantDigest string
		wantErr    bool
	}{
		{name: "happy path", tag: "promoted-abc1234-1.9.0", wantDigest: digest},
		{name: "tag not found", tag: "promoted-abc1234-9.9.9", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := client.ResolveByTag(tt.tag)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.Digest != tt.wantDigest {
				t.Errorf("digest: got %q, want %q", info.Digest, tt.wantDigest)
			}
		})
	}
}

func TestOCIRegistryClient_FindTagsByDigest(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/myimage"

	digestA := pushImage(t, host, repo, []string{"promoted-latest", "promoted-abc1234-1.9.0"})
	pushImage(t, host, repo, []string{"promoted-def5678-1.8.0"})

	client := NewOCIRegistry(srv.URL, repo)

	tags, err := client.FindTagsByDigest(digestA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := make(map[string]bool, len(tags))
	for _, tag := range tags {
		got[tag] = true
	}
	if !got["promoted-latest"] {
		t.Error("expected promoted-latest in results")
	}
	if !got["promoted-abc1234-1.9.0"] {
		t.Error("expected promoted-abc1234-1.9.0 in results")
	}
	if got["promoted-def5678-1.8.0"] {
		t.Error("expected promoted-def5678-1.8.0 NOT in results")
	}
}
