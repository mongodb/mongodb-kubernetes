package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestBackportFromCmd(t *testing.T) {
	tests := []struct {
		name    string
		arg     string
		json    bool
		want    string
		wantErr bool
	}{
		{name: "master prints next", arg: "master", want: "old-master"},
		{name: "last in chain prints nothing", arg: "old-master", want: ""},
		{name: "untracked errors", arg: "does-not-exist", wantErr: true},
		{
			name: "json prints full target entry",
			arg:  "master",
			json: true,
			want: `{"name":"old-master","description":"Test backporting branch","version":"0.x","internal-only":true}`,
		},
		{name: "json last in chain prints nothing", arg: "old-master", json: true, want: ""},
		{name: "json untracked errors", arg: "does-not-exist", json: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := NewRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			args := []string{"backport", "from", tt.arg}
			if tt.json {
				args = append(args, "--json")
			}
			root.SetArgs(args)

			err := root.Execute()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tt.arg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := strings.TrimSpace(out.String()); got != tt.want {
				t.Errorf("backport from %q = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestNewRoot_RegistersBackport(t *testing.T) {
	root := NewRoot()
	found := false
	for _, sub := range root.Commands() {
		if sub.Name() == "backport" {
			found = true
		}
	}
	if !found {
		t.Errorf("backport subcommand not registered on root")
	}
}
