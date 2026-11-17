package mongodbproxy

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"
)

// A request names files that the proxy reads or hands to mongod: the config file, the Kerberos keytab and the LDAP CA
// file. Whoever can call the API must not be able to point any of them at a file elsewhere in the container, so each
// kind has a list of directories it has to be in.
//
// The directories are enforced twice. The path is compared with them as text, and the file is then opened with
// os.Root, which follows no symlink and no ".." that leads out of the directory, and which is not fooled by a rename
// that happens while the path is walked. The text comparison alone would let a symlink inside /data point anywhere.

// PathPolicy lists the directories that the paths of a request have to be in.
type PathPolicy struct {
	// ConfigDirs is for configPath.
	ConfigDirs []string
	// FileDirs is for kerberosKeytab and ldapCaPath.
	FileDirs []string
}

// DefaultPathPolicy fits the pods of the operator: the agent writes the config to /data, and the secrets and the
// files that the agent writes for authentication are in the directories of the agent.
func DefaultPathPolicy() PathPolicy {
	return PathPolicy{
		ConfigDirs: []string{"/data"},
		FileDirs:   []string{"/var/lib/mongodb-automation", "/var/lib/mongodb-mms-automation", "/mongodb-automation", "/data"},
	}
}

// maxConfigBytes bounds what is read of a config file.
const maxConfigBytes = 1 << 20

// within returns the directory of dirs that contains path and the path below it. path has to be absolute and clean.
func within(dirs []string, path string) (dir, rel string, ok bool) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", false
	}
	for _, d := range dirs {
		d = filepath.Clean(d)
		if rel, found := strings.CutPrefix(path, strings.TrimSuffix(d, "/")+"/"); found && rel != "" {
			return d, rel, true
		}
	}
	return "", "", false
}

// checkLexically reports whether path is below one of dirs, as text.
func checkLexically(kind string, dirs []string, path string) error {
	if _, _, ok := within(dirs, path); !ok {
		return fmt.Errorf("%w: %s %q must be a clean absolute path in one of %v", ErrInvalidRequest, kind, path, dirs)
	}
	return nil
}

// openBelow opens the file at path, which has to be below one of dirs, without leaving the directory. The result is a
// regular file.
func openBelow(dirs []string, path string) (*os.File, error) {
	dir, rel, ok := within(dirs, path)
	if !ok {
		return nil, fmt.Errorf("%q is not below one of %v", path, dirs)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()

	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, fmt.Errorf("%q is not a regular file", path)
	}
	return f, nil
}

// checkFile validates a keytab or CA path. A file that does not exist yet is left to mongod, which reports it better;
// one that exists has to be a regular file that is reached without leaving the directory.
func (p PathPolicy) checkFile(kind, path string) error {
	if path == "" {
		return nil
	}
	if err := checkLexically(kind, p.FileDirs, path); err != nil {
		return err
	}
	f, err := openBelow(p.FileDirs, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidRequest, kind, err)
	}
	return f.Close()
}

// checkRequest validates the paths of a start request.
func (p PathPolicy) checkRequest(r StartRequest) error {
	if err := checkLexically("configPath", p.ConfigDirs, r.ConfigPath); err != nil {
		return err
	}
	if err := p.checkFile("kerberosKeytab", r.Other.KerberosKeytab); err != nil {
		return err
	}
	return p.checkFile("ldapCaPath", r.Other.LdapCaPath)
}

// readConfig reads the config file once and refuses it when it sets processManagement.fork. A forking mongod makes
// its parent exit with code 0 while the real process lives on outside the proxy's reach: the proxy would report an
// exit and the agent would start a second mongod on the same data.
//
// A missing file is left to mongod, as is one that cannot be parsed. A path that leads out of the directory is
// refused.
func (p PathPolicy) readConfig(configPath string) error {
	f, err := openBelow(p.ConfigDirs, configPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: configPath: %w", ErrInvalidRequest, err)
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes))
	if err != nil {
		return nil
	}
	var config struct {
		ProcessManagement struct {
			Fork bool `json:"fork"`
		} `json:"processManagement"`
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil
	}
	if config.ProcessManagement.Fork {
		return fmt.Errorf("%w: %s sets processManagement.fork, which is not supported: the proxy needs mongod in the foreground", ErrInvalidRequest, configPath)
	}
	return nil
}
