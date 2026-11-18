package mongodbproxy

import (
	"fmt"
	"path/filepath"
	"strings"
)

const binaryNumactl = "numactl"

// numaPolicyAuto is the NUMA policy the Agent sends for every start request. It selects the same numactl behavior
// as the Agent's direct-process path.
const numaPolicyAuto = "auto"

// reservedArg reports whether arg is a flag that the proxy owns. The proxy gives mongod its config file, and a
// forking mongod leaves the proxy's control, so the agent must not set either itself. Both the separate form
// ("--config path") and the "=" form ("--config=path") are reserved.
func reservedArg(arg string) bool {
	name, _, _ := strings.Cut(arg, "=")
	switch name {
	case "-f", "--config", "--fork":
		return true
	}
	return false
}

// launchSpec is a validated StartRequest turned into what exec needs.
type launchSpec struct {
	binary     string
	args       []string
	env        []string
	numactl    bool
	disableTHP bool
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
	if o.DisableTHP {
		env = append(env, "SET_THP_DISABLED=yes")
	}

	return launchSpec{binary: binary, args: args, env: env, numactl: o.NumaPolicy == numaPolicyAuto, disableTHP: o.DisableTHP}, nil
}

func (r StopRequest) validate() error {
	if r.ExecutionID == "" {
		return fmt.Errorf("%w: executionId is required", ErrInvalidRequest)
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
