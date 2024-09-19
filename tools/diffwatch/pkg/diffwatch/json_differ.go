package diffwatch

import (
	fmt "fmt"
	"github.com/yudai/gojsondiff"
	"github.com/yudai/gojsondiff/formatter"
)

// JsonDiffer is a stateful object maintaining the latest json object and performs diffs against it.
type JsonDiffer struct {
	// previous json
	oldFileMap map[string]interface{}
	// like grep -B
	linesBefore int
	// like grep -A
	linesAfter int
	// function called when there are diffs to be saved
	diffWriter WriteDiffFunc
	ignores    []string
}

type WriteDiffFunc func(string, string, string, bool) error

func NewJsonDiffer(linesBefore int, linesAfter int, diffWriter WriteDiffFunc, ignores []string) (*JsonDiffer, error) {
	return &JsonDiffer{
		linesAfter:  linesAfter,
		linesBefore: linesBefore,
		ignores:     ignores,
		oldFileMap:  map[string]interface{}{},
		diffWriter:  diffWriter,
	}, nil
}

// FileChangedHandler is invoked with parsed file content.
// The file doesn't necessarily need to contain different content.
// If not changed (e.g. when called by periodic file watcher) - nothing will happen.
func (j *JsonDiffer) FileChangedHandler(parsedFile ParsedFileWrapper) (bool, bool, error) {
	if parsedFile.err != nil {
		return false, false, parsedFile.err
	}

	config := formatter.AsciiFormatterConfig{
		ShowArrayIndex: false,
		Coloring:       true,
	}

	if len(parsedFile.content) == 0 {
		return false, false, fmt.Errorf("empty file")
	}

	differ := gojsondiff.New()
	diff := differ.CompareObjects(j.oldFileMap, parsedFile.contentAsMap)
	if !diff.Modified() {
		return false, false, nil
	}
	jsonFormatter := formatter.NewAsciiFormatter(j.oldFileMap, config)

	diffString, err := jsonFormatter.Format(diff)
	if err != nil {
		// shouldn't happen
		panic(err)
	}

	shortDiffString, shouldIgnoreDiff := Grep(diffString, coloredDiffRegexPattern, j.linesBefore, j.linesAfter, j.ignores)

	if len(diffString) > 0 {
		if err := j.diffWriter(parsedFile.content, diffString, shortDiffString, shouldIgnoreDiff); err != nil {
			return false, false, err
		}
	}

	j.oldFileMap = parsedFile.contentAsMap

	return true, shouldIgnoreDiff, nil
}
