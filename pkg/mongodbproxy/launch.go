package mongodbproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// allowedEnv lists the environment variables the agent may set for the child.
// GLIBC_TUNABLES carries the process-local THP settings.
var allowedEnv = map[string]struct{}{
	"KRB5_KTNAME":    {},
	"LDAPTLS_CACERT": {},
	"GLIBC_TUNABLES": {},
}

// launchSpec is a validated StartRequest turned into what exec needs.
type launchSpec struct {
	binary string
	args   []string
	env    []string
}

func (r StartRequest) validate() (launchSpec, error) {
	if !filepath.IsAbs(r.ConfigPath) {
		return launchSpec{}, fmt.Errorf("%w: configPath must be an absolute path", ErrInvalidRequest)
	}
	if r.Role != RoleMain && r.Role != RoleEphemeral {
		return launchSpec{}, fmt.Errorf("%w: role must be %q or %q", ErrInvalidRequest, RoleMain, RoleEphemeral)
	}
	binary := r.Other.Binary
	if binary == "" {
		binary = BinaryMongod
	}
	if binary != BinaryMongod && binary != BinaryMongos {
		return launchSpec{}, fmt.Errorf("%w: binary must be %q or %q", ErrInvalidRequest, BinaryMongod, BinaryMongos)
	}

	keys := make([]string, 0, len(r.Other.Env))
	for k := range r.Other.Env {
		if _, ok := allowedEnv[k]; !ok {
			return launchSpec{}, fmt.Errorf("%w: environment variable %q is not allowed", ErrInvalidRequest, k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	env := os.Environ()
	for _, k := range keys {
		env = append(env, k+"="+r.Other.Env[k])
	}

	args := append([]string{"-f", r.ConfigPath}, r.Other.Args...)
	return launchSpec{binary: binary, args: args, env: env}, nil
}

func (r StopRequest) validate() error {
	if r.Role != "" && r.Role != RoleMain && r.Role != RoleEphemeral {
		return fmt.Errorf("%w: role must be empty, %q or %q", ErrInvalidRequest, RoleMain, RoleEphemeral)
	}
	if r.TimeoutSeconds < 0 {
		return fmt.Errorf("%w: timeoutSeconds must not be negative", ErrInvalidRequest)
	}
	if r.TimeoutSeconds == 0 && !r.Force {
		return fmt.Errorf("%w: timeoutSeconds must be > 0 unless force is set", ErrInvalidRequest)
	}
	return nil
}
