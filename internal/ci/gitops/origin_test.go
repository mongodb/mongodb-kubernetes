package gitops

import (
	"strings"
	"testing"
)

func TestParseGitHubRepo(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{"ssh shorthand with .git", "git@github.com:josvazg/mongodb-kubernetes.git", "josvazg/mongodb-kubernetes", false},
		{"ssh shorthand no suffix", "git@github.com:josvazg/mongodb-kubernetes", "josvazg/mongodb-kubernetes", false},
		{"https with .git", "https://github.com/josvazg/mongodb-kubernetes.git", "josvazg/mongodb-kubernetes", false},
		{"https no suffix", "https://github.com/josvazg/mongodb-kubernetes", "josvazg/mongodb-kubernetes", false},
		{"http (allowed for completeness)", "http://github.com/josvazg/mongodb-kubernetes", "josvazg/mongodb-kubernetes", false},
		{"ssh url form", "ssh://git@github.com/josvazg/mongodb-kubernetes.git", "josvazg/mongodb-kubernetes", false},
		{"trailing whitespace tolerated", "  git@github.com:josvazg/mongodb-kubernetes.git\n", "josvazg/mongodb-kubernetes", false},
		{"trailing slash tolerated", "https://github.com/josvazg/mongodb-kubernetes/", "josvazg/mongodb-kubernetes", false},
		{"gitlab rejected", "https://gitlab.com/josvazg/mongodb-kubernetes", "", true},
		{"random string rejected", "not a url", "", true},
		{"empty rejected", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGitHubRepo(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got %q", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("ParseGitHubRepo(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestOwnerFromRepo(t *testing.T) {
	tests := []struct{ in, want string }{
		{"josvazg/mongodb-kubernetes", "josvazg"},
		{"mongodb/mongodb-kubernetes", "mongodb"},
		{"no-slash", ""},
		{"", ""},
		{"/repo", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := OwnerFromRepo(tt.in); got != tt.want {
				t.Errorf("OwnerFromRepo(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseGitHubRepo_ContainsURLOnError(t *testing.T) {
	_, err := ParseGitHubRepo("https://gitlab.com/x/y")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "gitlab.com") {
		t.Errorf("error should include the offending URL, got %v", err)
	}
}
