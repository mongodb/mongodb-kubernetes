package mongodbproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

const binaryNumactl = "numactl"

// replSetName limits the replica set name so that it can never be mistaken for a flag.
var replSetName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// launchSpec is a validated StartRequest turned into what exec needs.
type launchSpec struct {
	binary     string
	args       []string
	env        []string
	numactl    bool
	disableTHP bool
}

func (r StartRequest) validate() (launchSpec, error) {
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
	if o.ShardMaintenanceReplSet != "" && !replSetName.MatchString(o.ShardMaintenanceReplSet) {
		return launchSpec{}, fmt.Errorf("%w: shardMaintenanceReplSet %q is not a valid replica set name", ErrInvalidRequest, o.ShardMaintenanceReplSet)
	}
	for field, path := range map[string]string{"kerberosKeytab": o.KerberosKeytab, "ldapCaPath": o.LdapCaPath} {
		if path != "" && !filepath.IsAbs(path) {
			return launchSpec{}, fmt.Errorf("%w: %s must be an absolute path", ErrInvalidRequest, field)
		}
	}

	args := []string{"-f", r.ConfigPath}
	if o.ConfigExpandRest {
		args = append(args, "--configExpand", "rest")
	}
	if o.Restore {
		args = append(args, "--restore")
	}
	if o.ShardMaintenanceReplSet != "" {
		args = append(args, "--replicaSetConfigShardMaintenanceMode", "--replSet", o.ShardMaintenanceReplSet)
	}
	if o.MagicRestore {
		args = append(args, "--magicRestore")
	}
	if o.ConfigOnly {
		args = append(args, "--configOnly")
	}

	env := os.Environ()
	if o.KerberosKeytab != "" {
		env = append(env, "KRB5_KTNAME="+o.KerberosKeytab)
	}
	if o.LdapCaPath != "" {
		env = append(env, "LDAPTLS_CACERT="+o.LdapCaPath)
	}
	if o.RseqDisabled {
		env = append(env, "GLIBC_TUNABLES=glibc.pthread.rseq=0")
	}
	if o.DisableTHP {
		env = append(env, "SET_THP_DISABLED=yes")
	}

	return launchSpec{binary: binary, args: args, env: env, numactl: o.NumaInterleave, disableTHP: o.DisableTHP}, nil
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
