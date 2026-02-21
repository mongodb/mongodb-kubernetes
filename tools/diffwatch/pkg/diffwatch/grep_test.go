package diffwatch

import (
	"os"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGrepWithBeforeAndAfter(t *testing.T) {
	input :=
		`0 x
1 y
2 y
3 b
4 b
5 y
6 y
7 x
8 y
9 y
10 b`
	expectedOutput :=
		`1 y
2 y
3 b
4 b
5 y
6 y
--
8 y
9 y
10 b`

	actualOutput, _ := Grep(input, regexp.MustCompile("b"), 2, 2, nil)
	assert.Equal(t, expectedOutput, actualOutput)
}

func TestGrepWithoutBeforeAndAfter(t *testing.T) {
	input :=
		`0 x
1 x
2 x
3 b
4 b
5 x
6 x
7 x
8 x
9 x
10 b`

	// no separators when no linesBefore or after
	expectedOutput :=
		`3 b
4 b
10 b`

	actualOutput, _ := Grep(input, regexp.MustCompile("b"), 0, 0, nil)
	assert.Equal(t, expectedOutput, actualOutput)
}

func TestGrepWithoutBeforeAndWithAfter(t *testing.T) {
	input :=
		`0 x
1 b
2 y
3 y
4 y`
	expectedOutput :=
		`1 b
2 y
3 y
4 y`

	actualOutput, _ := Grep(input, regexp.MustCompile("b"), 0, 100, nil)
	assert.Equal(t, expectedOutput, actualOutput)
}

func TestGrepNoMatches(t *testing.T) {
	input :=
		`0 x
1 b
4 y`
	expectedOutput := ""

	actualOutput, _ := Grep(input, regexp.MustCompile("X"), 0, 100, nil)
	assert.Equal(t, expectedOutput, actualOutput)
}

func TestGrepAllMatches(t *testing.T) {
	input :=
		`0 x
1 b
4 y`
	expectedOutput := input

	actualOutput, _ := Grep(input, regexp.MustCompile("\\d\\s([xby])"), 0, 0, nil)
	assert.Equal(t, expectedOutput, actualOutput)
}

func TestGrepWithColorCodes(t *testing.T) {
	inputBytes, err := os.ReadFile("resources/json_with_colors.json")
	require.NoError(t, err)
	expectedOutputBytes, err := os.ReadFile("resources/json_with_colors_expected_output.json")
	require.NoError(t, err)
	actualOutput, _ := Grep(string(inputBytes), coloredDiffRegexPattern, 1, 1, nil)
	assert.Equal(t, string(expectedOutputBytes), actualOutput)
}

func TestGrepWithColorAndIgnores(t *testing.T) {
	inputBytes, err := os.ReadFile("resources/json_with_colors_ignored_only.json")
	require.NoError(t, err)
	expectedOutputBytes, err := os.ReadFile("resources/json_with_colors_ignored_only_expected_output.json")
	require.NoError(t, err)
	t.Run("check diff without ignores", func(t *testing.T) {
		actualOutput, actualShouldIgnoreDiff := Grep(string(inputBytes), coloredDiffRegexPattern, 1, 1, nil)
		assert.Equal(t, string(expectedOutputBytes), actualOutput)
		assert.False(t, actualShouldIgnoreDiff)
	})
	t.Run("check diff with incomplete ignores, so full diff is triggered", func(t *testing.T) {
		actualOutput, actualShouldIgnoreDiff := Grep(string(inputBytes), coloredDiffRegexPattern, 1, 1, []string{"mode", "disabled"})
		assert.Equal(t, string(expectedOutputBytes), actualOutput)
		assert.False(t, actualShouldIgnoreDiff)
	})

	t.Run("check diff with all ignores matched, diff should be ignored", func(t *testing.T) {
		actualOutput, actualShouldIgnoreDiff := Grep(string(inputBytes), coloredDiffRegexPattern, 1, 1, []string{"mode", "disabled", "timestamp"})
		assert.Equal(t, string(expectedOutputBytes), actualOutput)
		assert.True(t, actualShouldIgnoreDiff)
	})

	t.Run("check diff with all ignores including timestamp regex matches, diff should be ignored", func(t *testing.T) {
		actualOutput, actualShouldIgnoreDiff := Grep(string(inputBytes), coloredDiffRegexPattern, 1, 1, []string{"mode", "disabled", `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}`})
		assert.Equal(t, string(expectedOutputBytes), actualOutput)
		assert.True(t, actualShouldIgnoreDiff)
	})
}
