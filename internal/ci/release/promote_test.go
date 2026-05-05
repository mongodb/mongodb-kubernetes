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

func TestPromote(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	const repo = "myorg/myimage"

	// push a source "nightly" image
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("random image: %v", err)
	}
	srcDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("image digest: %v", err)
	}
	srcRef := fmt.Sprintf("%s/%s:nightly-abc1234", host, repo)
	ref, err := name.ParseReference(srcRef, name.Insecure)
	if err != nil {
		t.Fatalf("parse ref: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("push source: %v", err)
	}

	tests := []struct {
		name    string
		inputs  PromoteInputs
		wantErr string
	}{
		{
			name:   "happy path",
			inputs: PromoteInputs{Image: srcRef, Commit: "abc1234", Version: "1.9.0"},
		},
		{
			name:    "image required",
			inputs:  PromoteInputs{Commit: "abc1234", Version: "1.9.0"},
			wantErr: "image",
		},
		{
			name:    "commit required",
			inputs:  PromoteInputs{Image: srcRef, Version: "1.9.0"},
			wantErr: "commit",
		},
		{
			name:    "version required",
			inputs:  PromoteInputs{Image: srcRef, Commit: "abc1234"},
			wantErr: "version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			promoter := NewOCIPromoter(srv.URL, repo)
			tags, err := Promote(tt.inputs, promoter)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// both promoted tags must point to the same digest as the source
			reg := NewOCIRegistry(srv.URL, repo)
			for _, tag := range tags {
				info, err := reg.ResolveByTag(tag)
				if err != nil {
					t.Errorf("tag %s not found after promote: %v", tag, err)
					continue
				}
				if info.Digest != srcDigest.String() {
					t.Errorf("tag %s: digest got %q, want %q", tag, info.Digest, srcDigest.String())
				}
			}
		})
	}
}
