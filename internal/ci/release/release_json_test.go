package release

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTmp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "release.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	return path
}

func TestReadOperatorVersion(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		wantErr string
	}{
		{
			name:    "happy path",
			content: `{"mongodbOperator": "1.8.0", "other": "value"}`,
			want:    "1.8.0",
		},
		{
			name:    "missing field",
			content: `{"other": "value"}`,
			wantErr: "mongodbOperator field is missing",
		},
		{
			name:    "empty field",
			content: `{"mongodbOperator": ""}`,
			wantErr: "mongodbOperator field is missing",
		},
		{
			name:    "malformed json",
			content: `{not valid json`,
			wantErr: "parse",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTmp(t, tt.content)
			got, err := ReadOperatorVersion(path)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadOperatorVersion_FileMissing(t *testing.T) {
	_, err := ReadOperatorVersion(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "read") {
		t.Errorf("expected error mentioning 'read', got %v", err)
	}
}
