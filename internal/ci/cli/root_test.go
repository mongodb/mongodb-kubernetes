package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewRoot_RegistersExpectedSubcommands(t *testing.T) {
	root := NewRoot()

	want := map[string]bool{
		"version": false,
		"release": false,
	}
	for _, sub := range root.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q not registered on root", name)
		}
	}
}

func TestNewRoot_NoArgsPrintsHelp(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{})

	if err := root.Execute(); err != nil {
		t.Fatalf("executing root with no args returned error: %v", err)
	}
	if !strings.Contains(out.String(), "mckctl") {
		t.Errorf("expected help output to mention mckctl, got: %q", out.String())
	}
}

func TestVersionCmd_PrintsBuildInfo(t *testing.T) {
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"version"})

	if err := root.Execute(); err != nil {
		t.Fatalf("version subcommand returned error: %v", err)
	}
	if !strings.Contains(out.String(), "mckctl") {
		t.Errorf("expected version output to mention mckctl, got: %q", out.String())
	}
}
