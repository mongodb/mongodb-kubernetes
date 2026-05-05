package release

import (
	"errors"
	"strings"
	"testing"
)

// --- fakes ---

type fakeRegistry struct {
	tags     map[string]ImageInfo
	byDigest map[string][]string
	err      error
}

func (f *fakeRegistry) ResolveByTag(tag string) (ImageInfo, error) {
	if f.err != nil {
		return ImageInfo{}, f.err
	}
	if info, ok := f.tags[tag]; ok {
		return info, nil
	}
	return ImageInfo{}, errors.New("tag " + tag + " not found")
}

func (f *fakeRegistry) FindTagsByDigest(digest string) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byDigest[digest], nil
}

type fakeGit struct {
	commits map[string]bool
}

func (f *fakeGit) HasCommit(sha string) bool {
	return f.commits[sha]
}

// --- PromotedTagFor ---

func TestPromotedTagFor(t *testing.T) {
	tests := []struct {
		commit  string
		version string
		want    string
	}{
		{"abc1234", "1.9.0", "promoted-abc1234-1.9.0"},
		{"abc1234", "1.9.0-rc.1", "promoted-abc1234-1.9.0-rc.1"},
		{"abc1234def5", "1.10.0", "promoted-abc1234def5-1.10.0"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := PromotedTagFor(tt.commit, tt.version)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

// --- ParsePromotedTag ---

func TestParsePromotedTag(t *testing.T) {
	tests := []struct {
		tag        string
		wantCommit string
		wantVer    string
		wantErr    bool
	}{
		{tag: "promoted-abc1234-1.9.0", wantCommit: "abc1234", wantVer: "1.9.0"},
		{tag: "promoted-abc1234def5-1.10.0", wantCommit: "abc1234def5", wantVer: "1.10.0"},
		{tag: "promoted-abc1234-1.9.0-rc.1", wantCommit: "abc1234", wantVer: "1.9.0-rc.1"},
		{tag: "promoted-latest", wantErr: true},
		{tag: "promoted-abc1234", wantErr: true},
		{tag: "someother-abc1234-1.9.0", wantErr: true},
		{tag: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.tag, func(t *testing.T) {
			commit, ver, err := ParsePromotedTag(tt.tag)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrNotVersionedPromotedTag) {
					t.Errorf("expected ErrNotVersionedPromotedTag, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if commit != tt.wantCommit {
				t.Errorf("commit: got %q, want %q", commit, tt.wantCommit)
			}
			if ver != tt.wantVer {
				t.Errorf("version: got %q, want %q", ver, tt.wantVer)
			}
		})
	}
}

// --- Verify: latest mode ---

func TestVerify_LatestMode(t *testing.T) {
	const digest = "sha256:aabbcc"

	tests := []struct {
		name        string
		latestInfo  ImageInfo
		digestTags  []string
		registryErr error
		gitHas      []string
		version     string
		wantErr     string
	}{
		{
			name:       "happy path",
			latestInfo: ImageInfo{Digest: digest},
			digestTags: []string{"promoted-abc1234-1.9.0"},
			gitHas:     []string{"abc1234"},
			version:    "1.9.0",
		},
		{
			name:       "version mismatch",
			latestInfo: ImageInfo{Digest: digest},
			digestTags: []string{"promoted-abc1234-1.9.0"},
			gitHas:     []string{"abc1234"},
			version:    "1.8.0",
			wantErr:    "1.9.0",
		},
		{
			name:        "promoted-latest not found",
			registryErr: errors.New("tag promoted-latest not found"),
			version:     "1.9.0",
			wantErr:     "promoted-latest",
		},
		{
			name:       "no versioned tag for digest",
			latestInfo: ImageInfo{Digest: digest},
			digestTags: []string{},
			gitHas:     []string{},
			version:    "1.9.0",
			wantErr:    "no versioned promoted tag",
		},
		{
			name:       "commit not in git",
			latestInfo: ImageInfo{Digest: digest},
			digestTags: []string{"promoted-abc1234-1.9.0"},
			gitHas:     []string{},
			version:    "1.9.0",
			wantErr:    "abc1234",
		},
		{
			name:    "version required",
			version: "",
			wantErr: "version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tags := map[string]ImageInfo{}
			byDigest := map[string][]string{}
			if tt.registryErr == nil {
				tags[promotedLatestTag()] = tt.latestInfo
				byDigest[digest] = tt.digestTags
			}
			commits := make(map[string]bool, len(tt.gitHas))
			for _, c := range tt.gitHas {
				commits[c] = true
			}
			reg := &fakeRegistry{tags: tags, byDigest: byDigest, err: tt.registryErr}
			git := &fakeGit{commits: commits}

			_, err := Verify(VerifyInputs{Version: tt.version}, reg, git)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}

// --- Verify: explicit commit mode ---

func TestVerify_ExplicitCommitMode(t *testing.T) {
	tests := []struct {
		name    string
		tags    map[string]ImageInfo
		gitHas  []string
		commit  string
		version string
		wantErr string
	}{
		{
			name:    "happy path",
			tags:    map[string]ImageInfo{"promoted-abc1234-1.9.0": {}},
			gitHas:  []string{"abc1234"},
			commit:  "abc1234",
			version: "1.9.0",
		},
		{
			name:    "commit not promoted",
			tags:    map[string]ImageInfo{},
			gitHas:  []string{"abc1234"},
			commit:  "abc1234",
			version: "1.9.0",
			wantErr: "promoted-abc1234-1.9.0",
		},
		{
			name:    "commit not in git",
			tags:    map[string]ImageInfo{"promoted-abc1234-1.9.0": {}},
			gitHas:  []string{},
			commit:  "abc1234",
			version: "1.9.0",
			wantErr: "abc1234",
		},
		{
			name:    "version required",
			tags:    map[string]ImageInfo{"promoted-abc1234-1.9.0": {}},
			gitHas:  []string{"abc1234"},
			commit:  "abc1234",
			version: "",
			wantErr: "version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commits := make(map[string]bool, len(tt.gitHas))
			for _, c := range tt.gitHas {
				commits[c] = true
			}
			reg := &fakeRegistry{tags: tt.tags}
			git := &fakeGit{commits: commits}

			_, err := Verify(VerifyInputs{Version: tt.version, Commit: tt.commit}, reg, git)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("expected error containing %q, got %q", tt.wantErr, err.Error())
			}
		})
	}
}
