package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"github.com/mongodb/mongodb-kubernetes/diffwatch/pkg/diffwatch"
)

type arrayFlags []string

func (i *arrayFlags) String() string {
	return strings.Join(*i, ",")
}

func (i *arrayFlags) Set(value string) error {
	*i = append(*i, value)
	return nil
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-signalChan
		cancel()
	}()

	readFromStdin := isInPipeMode()
	var inputStream io.Reader
	if readFromStdin {
		inputStream = os.Stdin
	}

	var filePath string
	var destDir string
	var linesAfter int
	var linesBefore int
	var linesContext int
	var ignores arrayFlags
	flag.StringVar(&filePath, "file", "", "Path to the JSON file that will be periodically observed for changes. Optional when the content is piped on stdin. Required when -destDir is specified. "+
		"If reading from stdin, then path is not relevant (file won't be read), but the file name will be used for the diff files prefix stored in destDir.")
	flag.StringVar(&destDir, "destDir", "", "Path to the destination directory to store diffs. Optional. If not set, then diff files won't be created. "+
		"If specified, then -file parameter is required. The files will be prefixed with file name of the -file parameter.")
	flag.IntVar(&linesAfter, "A", 0, "Number of lines printed after a match (default 0)")
	flag.IntVar(&linesBefore, "B", 0, "Number of lines printed before a match (default 0)")
	flag.IntVar(&linesContext, "C", 3, "Number of context lines printed before and after (equivalent to setting -A and -B) (default = 3)")
	flag.Var(&ignores, "ignore", "Regex pattern to ignore triggering diff if the only changes are ignored ones; you can specify multiple --ignore parameters, e.g. --ignore timestamp --ignore '\\d{4}-\\d{2}-\\d{2}T\\d{2}:\\d{2}:\\d{2}\\.\\d{3}Z' (ignore all lines with changed timestamp)")
	flag.Parse()

	for ignore := range ignores {
		fmt.Println("ignore = ", ignore)
	}

	if linesBefore == 0 {
		linesBefore = linesContext
	}
	if linesAfter == 0 {
		linesAfter = linesContext
	}

	if err := watchChanges(ctx, filePath, destDir, inputStream, linesBefore, linesAfter, ignores); err != nil {
		cancel()
		if err == io.EOF {
			log.Printf("Reached end of stream. Exiting.")
		} else {
			log.Printf("Error: %v", err)
		}
		os.Exit(1)
	}
}

func isInPipeMode() bool {
	stat, _ := os.Stdin.Stat()
	return (stat.Mode() & os.ModeCharDevice) == 0
}

func watchChanges(ctx context.Context, filePath string, destDir string, inputStream io.Reader, linesBefore int, linesAfter int, ignores []string) error {
	diffWriterFunc := diffwatch.WriteDiffFiles(destDir, path.Base(filePath))
	jsonDiffer, err := diffwatch.NewJsonDiffer(linesBefore, linesAfter, diffWriterFunc, ignores)
	if err != nil {
		return err
	}

	// parsedFileChannel is filled in the background by reading from stream or watching the file periodically
	parsedFileChannel := make(chan diffwatch.ParsedFileWrapper)
	if inputStream != nil {
		go diffwatch.ReadAndParseFromStream(ctx, inputStream, filePath, parsedFileChannel)
	} else {
		go diffwatch.ReadAndParseFilePeriodically(ctx, filePath, diffwatch.DefaultWatchInterval, parsedFileChannel)
	}

	return diffwatch.WatchFileChangesPeriodically(ctx, filePath, parsedFileChannel, jsonDiffer.FileChangedHandler)
}
