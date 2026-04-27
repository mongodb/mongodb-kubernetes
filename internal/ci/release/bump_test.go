package release

import (
	"os"
	"strings"
	"testing"
)

func TestBumpOperatorVersion(t *testing.T) {
	tests := []struct {
		name        string
		initial     string
		target      string
		wantOld     string
		wantChanged bool
		wantContent string // empty = same as initial
		wantErr     string
	}{
		{
			name:        "happy path",
			initial:     `{"mongodbOperator": "1.7.0", "x": "y"}`,
			target:      "1.8.0",
			wantOld:     "1.7.0",
			wantChanged: true,
			wantContent: `{"mongodbOperator": "1.8.0", "x": "y"}`,
		},
		{
			name:    "already at target is a no-op",
			initial: `{"mongodbOperator": "1.8.0", "x": "y"}`,
			target:  "1.8.0",
			wantOld: "1.8.0",
		},
		{
			name:    "missing field",
			initial: `{"x": "y"}`,
			target:  "1.8.0",
			wantErr: "mongodbOperator field not found",
		},
		{
			name:        "preserves indentation",
			initial:     "{\n  \"mongodbOperator\": \"1.7.0\",\n  \"x\": \"y\"\n}",
			target:      "1.8.0",
			wantOld:     "1.7.0",
			wantChanged: true,
			wantContent: "{\n  \"mongodbOperator\": \"1.8.0\",\n  \"x\": \"y\"\n}",
		},
		{
			name:        "value containing dots",
			initial:     `{"mongodbOperator": "1.7.0-rc1"}`,
			target:      "1.8.0",
			wantOld:     "1.7.0-rc1",
			wantChanged: true,
			wantContent: `{"mongodbOperator": "1.8.0"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTmp(t, tt.initial)
			old, changed, err := BumpOperatorVersion(path, tt.target)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if old != tt.wantOld {
				t.Errorf("oldVersion: got %q, want %q", old, tt.wantOld)
			}
			if changed != tt.wantChanged {
				t.Errorf("changed: got %v, want %v", changed, tt.wantChanged)
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatalf("re-read tmp file: %v", readErr)
			}
			wantContent := tt.wantContent
			if wantContent == "" {
				wantContent = tt.initial
			}
			if string(content) != wantContent {
				t.Errorf("content:\n got: %q\nwant: %q", string(content), wantContent)
			}
		})
	}
}

func TestBumpOperatorVersion_FileMissing(t *testing.T) {
	_, _, err := BumpOperatorVersion(t.TempDir()+"/does-not-exist.json", "1.8.0")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
