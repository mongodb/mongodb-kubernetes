package main

import (
	"fmt"
	"github.com/mongodb/mongodb-kubernetes/pkg/util/env"
	"github.com/stretchr/testify/assert"
	"os"
	"strings"
	"testing"
)

func TestTemplates(t *testing.T) {
	templateData := TemplateData{
		Namespace:    "ns",
		ResourceName: "mdb",
		ResourceType: "mdb",
		StsName:      "mdb-0",
		PodName:      "mdb-0-1",
		PodIdx:       1,
		ClusterIdx:   0,
		ShortName:    "m",
	}
	var str string
	var err error

	templateFiles := []string{
		"appdb_entrypoint.sh.tpl",
		"appdb_tmux_session.yaml.tpl",
		"om_backup_daemon_entrypoint.sh.tpl",
		"om_backup_daemon_tmux_session.yaml.tpl",
		"om_entrypoint.sh.tpl",
		"om_tmux_session.yaml.tpl",
		"mongos_entrypoint.sh.tpl",
		"mongos_entrypoint.sh.tpl",
		"replicaset_entrypoint.sh.tpl",
		"replicaset_tmux_session.yaml.tpl",
	}

	projectDir := env.ReadOrDefault("PROJECT_DIR", "")
	var generatedDir string
	if projectDir != "" {
		generatedDir = fmt.Sprintf("%s/.generated/tmp", projectDir)
		if err := os.Mkdir(generatedDir, 0760); err != nil {
			fmt.Printf("Failed to create dir %s: %s\n", generatedDir, err)
		}
	}

	for _, templateFile := range templateFiles {
		str, err = renderTemplate(templateFile, templateData)
		if generatedDir != "" {
			filePath := fmt.Sprintf("%s/%s", generatedDir, strings.ReplaceAll(templateFile, ".tpl", ""))
			if err := os.WriteFile(filePath, []byte(str), 0660); err != nil {
				fmt.Printf("Failed to write file %s: %s", filePath, err)
			}
		}
		assert.NoError(t, err)
		assert.NotEmpty(t, str)
	}
}
