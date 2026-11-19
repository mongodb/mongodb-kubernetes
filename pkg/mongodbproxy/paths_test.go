package mongodbproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outside returns a file that is not in the directory of the policy, with a content that a test can look for.
func outside(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "outside")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

func TestWithin(t *testing.T) {
	dirs := []string{"/data", "/var/log/mongodb-mms-automation"}
	tests := map[string]struct {
		path string
		ok   bool
	}{
		"in the first directory":        {"/data/automation-mongod.conf", true},
		"in a subdirectory":             {"/data/sub/x.conf", true},
		"in the second directory":       {"/var/log/mongodb-mms-automation/mongodb.log", true},
		"the directory itself":          {"/data", false},
		"a directory with the prefix":   {"/data2/x.conf", false},
		"a sibling with the same start": {"/database/x.conf", false},
		"not clean (dot dot)":           {"/data/../etc/passwd", false},
		"not clean (dot)":               {"/data/./x.conf", false},
		"double slash":                  {"/data//x.conf", false},
		"trailing slash":                {"/data/x/", false},
		"relative":                      {"data/x.conf", false},
		"empty":                         {"", false},
		"elsewhere":                     {"/etc/passwd", false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, ok := within(dirs, tc.path)
			assert.Equal(t, tc.ok, ok)
		})
	}
}

func TestEnvValue(t *testing.T) {
	env := []string{"PATH=/bin", "A=1", "B=2=3", "A=4"}
	assert.Equal(t, "4", envValue(env, "A"), "a later entry wins for the child")
	assert.Equal(t, "2=3", envValue(env, "B"))
	assert.Equal(t, "", envValue(env, "C"))
	assert.Equal(t, "", envValue([]string{"A"}, "A"))
}

func TestOpenBelowDoesNotFollowASymlinkOutOfTheDirectory(t *testing.T) {
	dir := t.TempDir()
	secret := outside(t, "secret")
	require.NoError(t, os.Symlink(secret, filepath.Join(dir, "link")))
	require.NoError(t, os.Symlink(filepath.Dir(secret), filepath.Join(dir, "dirlink")))

	for name, path := range map[string]string{
		"a symlink to a file":      filepath.Join(dir, "link"),
		"a symlink to a directory": filepath.Join(dir, "dirlink", "outside"),
	} {
		t.Run(name, func(t *testing.T) {
			f, err := openBelow([]string{dir}, path)
			if f != nil {
				_ = f.Close()
			}
			assert.Error(t, err, "the file outside of the directory must not be opened")
		})
	}
}

func TestOpenBelowFollowsASymlinkInsideTheDirectory(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real"), []byte("x"), 0o600))
	require.NoError(t, os.Symlink("real", filepath.Join(dir, "link")))

	f, err := openBelow([]string{dir}, filepath.Join(dir, "link"))
	require.NoError(t, err)
	require.NoError(t, f.Close())
}

func TestOpenBelowRefusesWhatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub"), 0o700))

	_, err := openBelow([]string{dir}, filepath.Join(dir, "sub"))
	assert.ErrorContains(t, err, "not a regular file")
}

func TestStartRefusesPathsOutsideThePolicy(t *testing.T) {
	f := newFixture(t)
	cfg := f.config("main", "run")
	elsewhere := outside(t, "x")

	tests := map[string]StartRequest{
		"config outside":     {ConfigPath: elsewhere, Role: RoleMain},
		"config not clean":   {ConfigPath: f.dir + "/../" + filepath.Base(f.dir) + "/main.conf", Role: RoleMain},
		"keytab outside":     {ConfigPath: cfg, Role: RoleMain, Other: Other{Env: []string{"KRB5_KTNAME=" + elsewhere}}},
		"ldap CA outside":    {ConfigPath: cfg, Role: RoleMain, Other: Other{Env: []string{"LDAPTLS_CACERT=" + elsewhere}}},
		"keytab not clean":   {ConfigPath: cfg, Role: RoleMain, Other: Other{Env: []string{"KRB5_KTNAME=" + f.dir + "/../x"}}},
		"keytab is the root": {ConfigPath: cfg, Role: RoleMain, Other: Other{Env: []string{"KRB5_KTNAME=/"}}},
	}
	for name, req := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := f.sup.Start(req)
			assert.ErrorIs(t, err, ErrInvalidRequest)
			assert.Equal(t, StateIdle, f.sup.Status().State, "nothing may have been started")
		})
	}
}

func TestStartRefusesAConfigThatIsASymlinkOutOfTheDirectory(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(f.dir, "link.conf")
	require.NoError(t, os.Symlink(outside(t, "net:\n  port: 1\n"), link))

	_, err := f.sup.Start(StartRequest{ConfigPath: link, Role: RoleMain})

	assert.ErrorIs(t, err, ErrInvalidRequest)
	assert.Equal(t, StateIdle, f.sup.Status().State)
}

func TestStartRefusesAKeytabThatIsASymlinkOutOfTheDirectory(t *testing.T) {
	f := newFixture(t)
	link := filepath.Join(f.dir, "krb5.keytab")
	require.NoError(t, os.Symlink(outside(t, "key"), link))

	_, err := f.sup.Start(StartRequest{ConfigPath: f.config("main", "run"), Role: RoleMain, Other: Other{Env: []string{"KRB5_KTNAME=" + link}}})

	assert.ErrorIs(t, err, ErrInvalidRequest)
}

func TestStartLeavesAMissingKeytabToMongod(t *testing.T) {
	f := newFixture(t)
	e, err := f.sup.Start(StartRequest{
		ConfigPath: f.config("main", "env"),
		Role:       RoleMain,
		Other:      Other{Env: []string{"KRB5_KTNAME=" + filepath.Join(f.dir, "not-yet-there.keytab")}},
	})
	require.NoError(t, err)
	waitDone(t, e)
}
