package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/mongodb/mongodb-kubernetes/diffwatch/pkg/diffwatch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ignored project-root/tmp dir where test outputs will be stored
const tmpDir = "../../../../tmp/diffwatch"
const cleanupAfterTest = false

// TestDiffWatcherFromFile is a manual test that triggers simulated sequence of changes.
// Intended for manual inspection of files.
//
// How to run:
//  1. Create ops-manager-kubernetes/tmp directory
//  2. Comment t.Skip and run the test
//  3. View latest files:
//     find $(find tmp/diffwatch -d 1 -type d | sort -n | tail -n 1) -type f | sort -rV | fzf --preview 'cat {}'
func TestDiffWatcherFromFile(t *testing.T) {
	t.Skip("Test intended to manual run, comment skip to run")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, os.MkdirAll(tmpDir, 0770))
	tempDir, err := os.MkdirTemp(tmpDir, time.Now().Format("20060102_150405"))
	require.NoError(t, err)
	defer func() {
		if cleanupAfterTest {
			_ = os.RemoveAll(tempDir)
		}
	}()

	watchedFile := fmt.Sprintf("%s/watched.json", tempDir)
	go watchChanges(ctx, watchedFile, tempDir, nil, 4, 4, []string{"ignoredField", `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`})

	diffwatch.DefaultWatchInterval = time.Millisecond * 100
	diffwatch.DefaultProgressInterval = time.Millisecond * 200
	applyChange(t, "resources/base.json", watchedFile)
	applyChange(t, "resources/changed_1.json", watchedFile)
	applyChange(t, "resources/changed_2.json", watchedFile)
	time.Sleep(diffwatch.DefaultProgressInterval * 2)
	applyChange(t, "resources/changed_3.json", watchedFile)
	applyChange(t, "resources/changed_3_ignored_only.json", watchedFile)
	applyChange(t, "resources/changed_3_ignored_only_2.json", watchedFile)
	applyChange(t, "resources/changed_4_ignored_only_ts.json", watchedFile)
	applyChange(t, "resources/changed_4_ignored_only_ts_and_other.json", watchedFile)
	applyChange(t, "resources/changed_5.json", watchedFile)
}

func TestDiffWatcherFromStream(t *testing.T) {
	t.Skip("Test intended to manual run, comment skip to run")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	buf := bytes.Buffer{}
	files := []string{
		"resources/base.json",
		"resources/changed_1.json",
		"resources/changed_2.json",
		"resources/changed_3.json",
		"resources/changed_4_ignored_only_ts.json",
		"resources/changed_4_ignored_only_ts_and_other.json",
		"resources/changed_5.json",
	}
	for _, file := range files {
		fileBytes, err := os.ReadFile(file)
		require.NoError(t, err)
		buf.Write(fileBytes)
	}

	require.NoError(t, os.MkdirAll(tmpDir, 0770))
	tempDir, err := os.MkdirTemp(tmpDir, time.Now().Format("20060102_150405"))
	require.NoError(t, err)
	defer func() {
		if cleanupAfterTest {
			_ = os.RemoveAll(tempDir)
		}
	}()

	watchedFile := "watched.file"
	_ = watchChanges(ctx, watchedFile, tempDir, &buf, 2, 2, []string{"ignoredField", `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`})
	cancel()
	time.Sleep(time.Second * 1)
}

func applyChange(t *testing.T, srcFilePath string, dstFilePath string) {
	assert.NoError(t, copyFile(srcFilePath, dstFilePath, 0660))
	time.Sleep(diffwatch.DefaultWatchInterval * 2)
}

func copyFile(srcFilePath string, dstFilePath string, mode os.FileMode) error {
	source, err := os.Open(srcFilePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = source.Close()
	}()

	destination, err := os.OpenFile(dstFilePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		_ = destination.Close()
	}()

	_, err = io.Copy(destination, source)
	return err
}
