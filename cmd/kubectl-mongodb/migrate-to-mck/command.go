package migratetomck

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
	kubernetesClient "github.com/mongodb/mongodb-kubernetes/mongodb-community-operator/pkg/kube/client"
	"github.com/mongodb/mongodb-kubernetes/pkg/kube"
)

const defaultNamespace = "default"

// promptOutput is the writer used for interactive prompts. Override in tests to suppress stderr noise.
var promptOutput io.Writer = os.Stderr

var MigrateCmd = &cobra.Command{
	Use:   "migrate-to-mck",
	Short: "Migrate MongoDB deployments to Kubernetes",
	Long: `Generates Kubernetes Custom Resources from an Ops Manager/Cloud Manager automation
config for migrating existing deployments to the operator.

Use one of the subcommands to generate specific resource types:
  mongodb  Generate a MongoDB CR
  users    Generate MongoDBUser CRs`,
}

func init() {
	MigrateCmd.AddCommand(MongodbCmd)
	MigrateCmd.AddCommand(UsersCmd)
}

func fetchAndValidate(conn om.Connection) (*om.AutomationConfig, *ProjectConfigs, *om.Process, error) {
	ac, err := conn.ReadAutomationConfig()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to read automation config: %w", err)
	}
	projectConfigs, err := readProjectConfigs(conn)
	if err != nil {
		return nil, nil, nil, err
	}
	results, sourceProcess := ValidateMigration(ac, ac.Deployment.ProcessMap(), projectConfigs)
	if errorCount := printValidationResults(os.Stderr, results); errorCount > 0 {
		return nil, nil, nil, fmt.Errorf("validation failed: %d error(s) found. Resolve the issues above before generating Custom Resources", errorCount)
	}
	return ac, projectConfigs, sourceProcess, nil
}

func printValidationResults(w io.Writer, results []ValidationResult) int {
	errorCount := 0
	for _, r := range results {
		if r.Severity == SeverityError {
			errorCount++
		}
		_, _ = fmt.Fprintf(w, "[%s] %s\n\n", r.Severity, r.Message)
	}
	return errorCount
}

func writeOutput(resources, outputFile string) error {
	if outputFile == "" {
		_, err := fmt.Fprint(os.Stdout, resources)
		return err
	}
	f, err := os.OpenFile(outputFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open output file %q: %w", outputFile, err)
	}
	defer func() { _ = f.Close() }()
	if _, err := fmt.Fprint(f, resources); err != nil {
		return fmt.Errorf("failed to write output: %w", err)
	}
	fmt.Fprintf(os.Stderr, "CRs written to %s\n", outputFile)
	return nil
}

// requirePasswordSecret fetches secretName and returns its "password" key bytes.
// Returns an error if the secret does not exist or is missing the key.
func requirePasswordSecret(ctx context.Context, kubeClient kubernetesClient.Client, namespace, secretName string) ([]byte, error) {
	secret, err := kubeClient.GetSecret(ctx, kube.ObjectKey(namespace, secretName))
	if err != nil {
		return nil, fmt.Errorf("secret %q not found in namespace %q: %w", secretName, namespace, err)
	}
	passwordBytes, ok := secret.Data[passwordSecretDataKey]
	if !ok {
		return nil, fmt.Errorf("secret %q does not contain key \"password\"", secretName)
	}
	return passwordBytes, nil
}

// promptKubernetesName prompts for a Kubernetes DNS name, re-prompting on empty or invalid input.
// If suggested is non-empty it is used as the default when the user presses Enter.
func promptKubernetesName(scanner *bufio.Scanner, prompt, suggested string) (string, error) {
	for {
		p, err := promptLine(scanner, prompt)
		if err != nil {
			return "", err
		}
		if p == "" {
			if suggested != "" {
				return suggested, nil
			}
			_, _ = fmt.Fprintln(promptOutput, "Value cannot be empty, please try again.")
			continue
		}
		if errs := k8svalidation.IsDNS1123Subdomain(p); len(errs) > 0 {
			_, _ = fmt.Fprintf(promptOutput, "%q is not a valid Kubernetes name: %s. Please try again.\n", p, errs[0])
			continue
		}
		return p, nil
	}
}

func promptLine(scanner *bufio.Scanner, prompt string) (string, error) {
	_, _ = fmt.Fprint(promptOutput, prompt)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("input cancelled")
	}
	return strings.TrimSpace(scanner.Text()), nil
}
