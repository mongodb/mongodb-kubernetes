package mongodbproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDisableTHPIsInheritedByChild checks the real prctl flag through /proc/<pid>/status.
func TestDisableTHPIsInheritedByChild(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\ngrep THP_enabled /proc/self/status\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, BinaryMongod), []byte(script), 0o755)) //nolint:gosec
	cfg := filepath.Join(dir, "main.conf")
	require.NoError(t, os.WriteFile(cfg, []byte("x"), 0o600))

	for _, disable := range []bool{true, false} {
		sup := NewSupervisor(WithBinDir(dir), WithOutput(&syncBuffer{}, &syncBuffer{}))
		e, err := sup.Start(StartRequest{ConfigPath: cfg, Role: RoleMain, Other: Other{DisableTHP: disable}})
		require.NoError(t, err)
		waitDone(t, e)

		want := "THP_enabled:\t1"
		if disable {
			want = "THP_enabled:\t0"
		}
		assert.Contains(t, sup.Status().Output, want, "disableTHP=%v", disable)
	}
}
