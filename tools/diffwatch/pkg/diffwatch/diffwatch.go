package diffwatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"regexp"
	"time"
)

var coloredDiffRegexPattern = regexp.MustCompile(`^\x1b(\[[0-9;]*m)+([+-])(.*)$`)

var DefaultWatchInterval = time.Second * 3
var DefaultProgressInterval = time.Second * 10

// WatchFileChangesPeriodically receives json file events on parsedFileChannel and invokes jsonDiffer.FileChangedHandler with it
func WatchFileChangesPeriodically(ctx context.Context, filePath string, parsedFileChannel <-chan ParsedFileWrapper, fileChangedHandler func(ParsedFileWrapper) (bool, bool, error)) error {
	progressMsgTicker := time.NewTicker(DefaultProgressInterval)
	defer progressMsgTicker.Stop()

	watchingMsgFunc := func() {
		fmt.Printf("%s Watching %s: ", time.Now().Format("2006/01/02 15:04:05"), filePath)
	}

	watchingMsgProgressFunc := func() {
		fmt.Printf(".")
	}
	watchingMsgProgressForIgnoredFunc := func() {
		fmt.Printf("x")
	}

	watchIntervalCallback := func(parsedFile ParsedFileWrapper) {
		modified, shouldIgnoreDiff, err := fileChangedHandler(parsedFile)
		if err != nil {
			fmt.Println()
			fmt.Printf("%v", err)
		}

		if modified && !shouldIgnoreDiff {
			watchingMsgFunc()
		}
		if shouldIgnoreDiff {
			watchingMsgProgressForIgnoredFunc()
		}
	}

	watchingMsgFunc()
	for {
		select {
		case parsedFile := <-parsedFileChannel:
			if parsedFile.err != nil {
				return parsedFile.err
			} else {
				watchIntervalCallback(parsedFile)
			}
		case <-ctx.Done():
			fmt.Println()
			log.Println("exiting WatchFileChangesPeriodically routine")
			return nil
		case <-progressMsgTicker.C:
			watchingMsgProgressFunc()
		}
	}
}

func WriteDiffFiles(destDir, fileName string) WriteDiffFunc {
	counter := 0
	return func(currentSourceFileString, fullDiffString, shortDiffString string, shouldIgnoreDiff bool) error {
		if destDir != "" {
			timestampStr := currentTimeStampString()
			fullDiffFileName := fmt.Sprintf("%s/%s_diff_full_%s_%d%s", destDir, fileName, timestampStr, counter, path.Ext(fileName))
			shortDiffFileName := fmt.Sprintf("%s/%s_diff_short_%s_%d%s", destDir, fileName, timestampStr, counter, path.Ext(fileName))
			currentSourceFileName := fmt.Sprintf("%s/%s_%s_%d%s", destDir, fileName, timestampStr, counter, path.Ext(fileName))
			counter++

			if err := os.WriteFile(currentSourceFileName, []byte(currentSourceFileString), 0644); err != nil {
				fmt.Println(err)
			}

			if err := os.WriteFile(fullDiffFileName, []byte(fullDiffString), 0644); err != nil {
				fmt.Println(err)
			}

			if !shouldIgnoreDiff {
				if err := os.WriteFile(shortDiffFileName, []byte(shortDiffString), 0644); err != nil {
					fmt.Println(err)
				}
			}
		}

		if !shouldIgnoreDiff {
			fmt.Println()
			fmt.Printf(shortDiffString)
		}

		return nil
	}
}

func currentTimeStampString() string {
	return time.Now().Format("20060102_15_04_05")
}
