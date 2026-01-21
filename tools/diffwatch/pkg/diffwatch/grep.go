package diffwatch

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Grep implements grepping str with specified context window.
// Similar to grep -A linesAfterMatch -B linesBeforeMatch.
func Grep(str string, pattern *regexp.Regexp, linesBeforeMatch int, linesAfterMatch int, ignores []string) (string, bool) {
	if len(str) == 0 {
		return "", false
	}
	if linesBeforeMatch < 0 {
		linesBeforeMatch = 0
	}
	if linesAfterMatch < 0 {
		linesAfterMatch = 0
	}

	var ignoreRegex []*regexp.Regexp
	for _, ignore := range ignores {
		r, err := regexp.Compile(ignore)
		if err != nil {
			panic(fmt.Errorf("error compiling pattern: %s: %w", ignore, err))
		}
		ignoreRegex = append(ignoreRegex, r)
	}

	trailingNewline := str[len(str)-1] == '\n'

	// contains indexes where there is a match
	var matchIndexes []int
	var ignoredIndexes []int
	indexesMatchedBy := map[int]map[*regexp.Regexp]struct{}{}
	lines := strings.Split(str, "\n")
	for idx := 0; idx < len(lines); idx++ {
		if matches := pattern.FindStringSubmatch(lines[idx]); matches != nil {
			matchIndexes = append(matchIndexes, idx)
			if len(matches) < 3 {
				continue
			}

			isRemoval := matches[2] == "-"
			restOfTheLine := matches[3]
			for _, r := range ignoreRegex {
				if r.MatchString(restOfTheLine) {
					if isRemoval {
						if indexesMatchedBy[idx] == nil {
							indexesMatchedBy[idx] = map[*regexp.Regexp]struct{}{}
						}
						indexesMatchedBy[idx][r] = struct{}{}
					} else if previousLineMatchedRegexes, ok := indexesMatchedBy[idx-1]; ok {
						if _, ok := previousLineMatchedRegexes[r]; ok {
							// previous line was matched by the same regex and it was removal
							// so we have consecutive removal and addition with the same match so we can ignore it
							ignoredIndexes = append(ignoredIndexes, idx-1, idx)
						}
					}
				}
			}
		}
	}

	shouldIgnoreDiff := slices.Equal(matchIndexes, ignoredIndexes)

	if len(matchIndexes) == 0 {
		return "", shouldIgnoreDiff
	}

	printSeparator := false
	strBuilder := strings.Builder{}
	currentMatchIndex := 0
	for idx := 0; idx < len(lines); idx++ {
		if idx > matchIndexes[currentMatchIndex]+linesAfterMatch {
			// if we're outside print window of the current match we need to jump to the next match index
			if currentMatchIndex < len(matchIndexes)-1 {
				currentMatchIndex++
			} else {
				// nothing will be printed from now on because there are no other matches
				break
			}
		} else if currentMatchIndex < len(matchIndexes)-1 && idx == matchIndexes[currentMatchIndex+1] {
			// move the current match if we're at the index where there is another match
			currentMatchIndex++
		}

		printLine := false
		// are we in "before" window
		if idx <= matchIndexes[currentMatchIndex] && idx >= (matchIndexes[currentMatchIndex]-linesBeforeMatch) {
			printLine = true
		}
		// are we in "after" window
		if idx >= matchIndexes[currentMatchIndex] && idx <= (matchIndexes[currentMatchIndex]+linesAfterMatch) {
			printLine = true
		}

		if printLine {
			strBuilder.WriteString(lines[idx])
			if idx != len(lines)-1 || trailingNewline {
				strBuilder.WriteString("\n")
			}
			// trigger printing match separator only after a match
			printSeparator = true
		} else if (linesBeforeMatch > 0 || linesAfterMatch > 0) && printSeparator {
			// we left the context window and there will be another matches, so we need to put the separator
			strBuilder.WriteString("--\n")
			// print separator once, it will be enabled again when another match is found
			printSeparator = false
		}
	}

	return strBuilder.String(), shouldIgnoreDiff
}
