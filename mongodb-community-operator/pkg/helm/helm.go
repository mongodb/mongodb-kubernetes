package helm

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// Uninstall uninstalls a helm chart of the given name. There is no error in the case
// of the helm chart not existing.
func Uninstall(t *testing.T, chartName string, namespace string) error {
	helmArgs := []string{"uninstall", chartName, "-n", namespace}
	return executeHelmCommand(t, helmArgs, isNotFoundMessage)
}

// Install a helm chert at the given path with the given name and the provided set arguments.
func Install(t *testing.T, chartPath, chartName string, flags, templateValues map[string]string) error {
	// let's ensure we never send telemetry by accident from community
	templateValues["operator.env"] = "dev"
	templateValues["operator.telemetry.send.enabled"] = strconv.FormatBool(false)
	templateValues["operator.telemetry.send.baseUrl"] = "https://cloud-dev.mongodb.com/"

	helmArgs := []string{"install"}
	helmArgs = append(helmArgs, chartName, chartPath)
	for flagKey, flagValue := range flags {
		helmArgs = append(helmArgs, fmt.Sprintf("--%s", flagKey))
		if flagValue != "" {
			helmArgs = append(helmArgs, flagValue)
		}
	}
	templateValues["operator.watchedResources"] = "{opsmanagers,mongodb,mongodbusers,mongodbcommunity}"
	helmArgs = append(helmArgs, mapToHelmValuesArg(templateValues)...)
	return executeHelmCommand(t, helmArgs, nil)
}

func isNotFoundMessage(s string) bool {
	return strings.Contains(s, "not found")
}

// executeHelmCommand accepts a list of arguments that should be passed to the helm command
// and a predicate that when returning true, indicates that the error message should be ignored.
func executeHelmCommand(t *testing.T, args []string, messagePredicate func(string) bool) error {
	cmd := exec.Command("helm", args...)
	t.Logf("cmd: %s", cmd.String())
	output, err := cmd.CombinedOutput()
	t.Logf("output: %s", output)
	if err != nil {
		if messagePredicate != nil && messagePredicate(string(output)) {
			return nil
		}
		return fmt.Errorf("error executing command: %s %s", err, output)
	}
	return nil
}

// mapToHelmValuesArg accepts a map of string to string and returns a list of arguments
// that can be passed to a shell helm command.
func mapToHelmValuesArg(m map[string]string) []string {
	var args []string
	for k, v := range m {
		args = append(args, "--set", fmt.Sprintf("%s=%s", k, v))
	}
	return args
}
