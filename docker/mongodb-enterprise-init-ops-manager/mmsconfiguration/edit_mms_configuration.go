package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"golang.org/x/xerrors"
)

const (
	mmsJvmParamsVar          = "JAVA_MMS_UI_OPTS"
	backupDaemonJvmParamsVar = "JAVA_DAEMON_OPTS"
	omPropertyPrefix         = "OM_PROP_"
	lineBreak                = "\n"
	commentPrefix            = "#"
	propOverwriteFmt         = "%s=\"${%s} %s\""
	backupDaemon             = "BACKUP_DAEMON"
	// keep in sync with AppDBConnectionStringPath constant from "github.com/10gen/ops-manager-kubernetes/controllers/operator/construct" package.
	// currently we cannot reference code from outside of docker/mongodb-enterprise-init-ops-manager
	// because this folder is set as the docker build context (configured in inventories/init_om.yaml)
	appDbConnectionStringPath     = "/mongodb-ops-manager/.mongodb-mms-connection-string"
	appDbConnectionStringFilePath = appDbConnectionStringPath + "/connectionString"
	// keep in sync with MmsMongoUri constant from github.com/10gen/ops-manager-kubernetes/pkg/util
	appDbUriKey = "mongo.mongoUri"
)

func updateConfFile(confFile string) error {
	confFilePropertyName := mmsJvmParamsVar
	var isBackupDaemon bool
	if _, isBackupDaemon = os.LookupEnv(backupDaemon); isBackupDaemon { // nolint:forbidigo
		confFilePropertyName = backupDaemonJvmParamsVar
	}

	customJvmParamsVar := "CUSTOM_" + confFilePropertyName
	jvmParams, jvmParamsEnvVarExists := os.LookupEnv(customJvmParamsVar) // nolint:forbidigo

	if !jvmParamsEnvVarExists || jvmParams == "" {
		fmt.Printf("%s not specified, not modifying %s\n", customJvmParamsVar, confFile)
		return nil
	}

	if isBackupDaemon {
		fqdn, err := getHostnameFQDN()
		if err == nil {
			// We need to add hostname to the Backup daemon
			jvmParams += " -Dmms.system.hostname=" + fqdn
		} else {
			fmt.Printf("was not able to get fqdn of the pod: %s\n", err)
		}
	}

	newMmsJvmParams := fmt.Sprintf(propOverwriteFmt, confFilePropertyName, confFilePropertyName, jvmParams)
	fmt.Printf("Appending %s to %s\n", newMmsJvmParams, confFile)

	return appendLinesToFile(confFile, getJvmParamDocString()+newMmsJvmParams+lineBreak)
}

// getHostnameFQDN returns the FQDN name for this Pod, which is the Pod's hostname
// and complete Domain.
//
// We use the pods hostname as the base and calculate which one _is the FQDN_ by
// a simple heuristic:
//
// - the longest string with _dots_ in it should be the FQDN.
// The output should match the shell call: hostname -f
func getHostnameFQDN() (string, error) {
	// Get the pod's hostname
	hostname, err := os.Hostname()
	if err != nil {
		return "", err
	}

	// Look up the pod's hostname in DNS
	addresses, err := net.LookupHost(hostname)
	if err != nil {
		return "", err
	}

	longestFQDN := ""

	for _, address := range addresses {
		// Get the pod's FQDN from the IP address
		fqdnList, err := net.LookupAddr(address)
		if err != nil {
			return "", err
		}

		for _, fqdn := range fqdnList {
			// Only consider fqdns with '.' on it
			if !strings.Contains(fqdn, ".") {
				continue
			}
			if len(fqdn) > len(longestFQDN) {
				longestFQDN = fqdn
			}
		}

	}

	if longestFQDN == "" {
		return "", errors.New("could not find FQDN for this host")
	}

	// Remove the trailing ".", If in there
	return strings.TrimRight(longestFQDN, "."), nil
}

func getMmsProperties() (map[string]string, error) {
	newProperties := getOmPropertiesFromEnvVars()

	appDbConnectionString, err := os.ReadFile(appDbConnectionStringFilePath)
	if err != nil {
		return nil, err
	}
	newProperties[appDbUriKey] = string(appDbConnectionString)
	// Enable dualConnectors to allow the kubelet to perform health checks through HTTP
	newProperties["mms.https.dualConnectors"] = "true"

	return newProperties, nil
}

func updatePropertiesFile(propertiesFile string, newProperties map[string]string) error {
	lines, err := readLinesFromFile(propertiesFile)
	if err != nil {
		return err
	}

	lines = updateMmsProperties(lines, newProperties)
	fmt.Printf("Updating configuration properties file %s\n", propertiesFile)
	err = writeLinesToFile(propertiesFile, lines)
	return err
}

func readLinesFromFile(name string) ([]string, error) {
	input, err := os.ReadFile(name)
	if err != nil {
		return nil, xerrors.Errorf("error reading file %s: %w", name, err)
	}
	return strings.Split(string(input), lineBreak), nil
}

func writeLinesToFile(name string, lines []string) error {
	output := strings.Join(lines, lineBreak)

	err := os.WriteFile(name, []byte(output), 0o775)
	if err != nil {
		return xerrors.Errorf("error writing to file %s: %w", name, err)
	}
	return nil
}

func appendLinesToFile(name string, lines string) error {
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return xerrors.Errorf("error opening file %s: %w", name, err)
	}

	if _, err = f.WriteString(lines); err != nil {
		return xerrors.Errorf("error writing to file %s: %w", name, err)
	}

	err = f.Close()
	return err
}

func getOmPropertiesFromEnvVars() map[string]string {
	props := map[string]string{}
	for _, pair := range os.Environ() {
		if !strings.HasPrefix(pair, omPropertyPrefix) {
			continue
		}

		p := strings.SplitN(pair, "=", 2)
		key := strings.Replace(p[0], omPropertyPrefix, "", 1)
		key = strings.ReplaceAll(key, "_", ".")
		props[key] = p[1]
	}
	return props
}

func updateMmsProperties(lines []string, newProperties map[string]string) []string {
	seenProperties := map[string]bool{}

	// Overwrite existing properties
	for i, line := range lines {
		if strings.HasPrefix(line, commentPrefix) || !strings.Contains(line, "=") {
			continue
		}

		key := strings.Split(line, "=")[0]
		if newVal, ok := newProperties[key]; ok {
			lines[i] = fmt.Sprintf("%s=%s", key, newVal)
			seenProperties[key] = true
		}
	}

	// Add new properties
	for key, val := range newProperties {
		if _, ok := seenProperties[key]; !ok {
			lines = append(lines, fmt.Sprintf("%s=%s", key, val))
		}
	}
	return lines
}

func getJvmParamDocString() string {
	commentMarker := strings.Repeat("#", 55)
	return fmt.Sprintf("%s\n## This is the custom JVM configuration set by the Operator\n%s\n\n", commentMarker, commentMarker)
}

func main() {
	if len(os.Args) < 3 {
		fmt.Printf("Incorrect arguments %s, must specify path to conf file and path to properties file"+lineBreak, os.Args[1:])
		os.Exit(1)
	}
	confFile := os.Args[1]
	propertiesFile := os.Args[2]
	if err := updateConfFile(confFile); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	newProperties, err := getMmsProperties()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	if err := updatePropertiesFile(propertiesFile, newProperties); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
