package migratetomck

import (
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/mongodb/mongodb-kubernetes/controllers/om"
)

const defaultNamespace = "default"

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
