package mongodbproxy

import (
	"fmt"
	"path/filepath"
	"strings"
)

// reservedArg reports whether arg is a flag that the proxy owns. The proxy gives mongod its config file, and a
// forking mongod leaves the proxy's control, so the agent must not set either itself. mongod accepts the short
// form, the single-dash and the double-dash long forms, with the value separate ("-f path"), attached
// ("-f/path") or after "=" ("-f=path").
func reservedArg(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch name {
	case "-f", "--config", "-config", "--fork", "-fork":
		return true
	}
	// The attached form carries the path in the same argument: -f/abs/path.conf.
	return strings.HasPrefix(name, "-f") && !strings.HasPrefix(name, "--")
}

// launchSpec is a validated StartRequest turned into what exec needs.
type launchSpec struct {
	binary string
	args   []string
	env    []string
}

// validate checks the request and turns it into a launch spec. baseEnv is the environment the child starts with,
// plus the variables that the request adds.
func (r StartRequest) validate(baseEnv []string) (launchSpec, error) {
	if !filepath.IsAbs(r.ConfigPath) {
		return launchSpec{}, fmt.Errorf("%w: configPath must be an absolute path", ErrInvalidRequest)
	}
	if r.Role != RoleMain && r.Role != RoleEphemeral {
		return launchSpec{}, fmt.Errorf("%w: role must be %q or %q", ErrInvalidRequest, RoleMain, RoleEphemeral)
	}
	o := r.Other
	// The executable name is picked from constants instead of being validated and reused: the string
	// from the request must not flow into exec.Command (CodeQL go/command-injection tracks that flow).
	var binary string
	switch o.Binary {
	case "", BinaryMongod:
		binary = BinaryMongod
	case BinaryMongos:
		binary = BinaryMongos
	default:
		return launchSpec{}, fmt.Errorf("%w: binary must be %q or %q", ErrInvalidRequest, BinaryMongod, BinaryMongos)
	}
	for _, arg := range o.Args {
		if reservedArg(arg) {
			return launchSpec{}, fmt.Errorf("%w: args must not contain %q: the proxy gives mongod its config file and keeps it in the foreground", ErrInvalidRequest, arg)
		}
	}

	args := append([]string{"-f", r.ConfigPath}, o.Args...)
	env := append(append([]string(nil), baseEnv...), o.Env...)
	env = append(env, "MONGODB_CONFIG_OVERRIDE_NOFORK=1")

	return launchSpec{binary: binary, args: args, env: env}, nil
}

// envValue returns the last value for key, matching exec's treatment of duplicate keys.
func envValue(env []string, key string) string {
	value := ""
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok && k == key {
			value = v
		}
	}
	return value
}

func (r StopRequest) validate() error {
	if r.PID <= 0 {
		return fmt.Errorf("%w: pid must be positive", ErrInvalidRequest)
	}
	if r.Role != RoleMain && r.Role != RoleEphemeral {
		return fmt.Errorf("%w: role must be %q or %q", ErrInvalidRequest, RoleMain, RoleEphemeral)
	}
	if r.TimeoutSeconds < 0 {
		return fmt.Errorf("%w: timeoutSeconds must not be negative", ErrInvalidRequest)
	}
	if r.TimeoutSeconds == 0 && !r.Force {
		return fmt.Errorf("%w: timeoutSeconds must be > 0 unless force is set", ErrInvalidRequest)
	}
	return nil
}
