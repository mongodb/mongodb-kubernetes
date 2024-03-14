package placeholders

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReplacer_Process(t *testing.T) {
	tests := []struct {
		name                 string
		placeholders         map[string]string
		input                string
		expectedReplacedFlag bool
		expectedOutput       string
		expectedErrMsg       string
	}{
		{
			name:                 "All placeholders replaced",
			placeholders:         map[string]string{"val1": "1", "val2": "12"},
			input:                "this is {val1} and {val2}",
			expectedOutput:       "this is 1 and 12",
			expectedReplacedFlag: true,
			expectedErrMsg:       "",
		},
		{
			name:                 "All placeholders replaced, some multiple times",
			placeholders:         map[string]string{"val1": "v1", "val2": "v2", "number": "3"},
			input:                "this is {val1} and {val2},{val2},{val2} (repeated {number} times)",
			expectedOutput:       "this is v1 and v2,v2,v2 (repeated 3 times)",
			expectedReplacedFlag: true,
			expectedErrMsg:       "",
		},
		{
			name:                 "No changes when no placeholders in the input",
			placeholders:         map[string]string{"val1": "v1"},
			input:                "no placeholders here",
			expectedOutput:       "no placeholders here",
			expectedReplacedFlag: false,
			expectedErrMsg:       "",
		},
		{
			name:                 "Works for empty strings",
			placeholders:         map[string]string{"val1": "v1"},
			input:                "",
			expectedOutput:       "",
			expectedReplacedFlag: false,
			expectedErrMsg:       "",
		},
		{
			name:                 "Works for empty strings with empty placeholder values",
			placeholders:         map[string]string{},
			input:                "",
			expectedOutput:       "",
			expectedReplacedFlag: false,
			expectedErrMsg:       "",
		},
		{
			name:                 "Invalid placeholders are ignored",
			placeholders:         map[string]string{},
			input:                `{inv@lid},{inv alid},{inv-alid}, {"json": {"string": }}, {{{{`,
			expectedOutput:       `{inv@lid},{inv alid},{inv-alid}, {"json": {"string": }}, {{{{`,
			expectedReplacedFlag: false,
			expectedErrMsg:       "",
		},
		{
			name:                 "Empty string values are allowed",
			placeholders:         map[string]string{"empty1": "", "empty2": ""},
			input:                "{empty1}{empty2}",
			expectedOutput:       "",
			expectedReplacedFlag: true,
			expectedErrMsg:       "",
		},
		{
			name:                 "Missing placeholder values are not allowed",
			placeholders:         map[string]string{"val1": "v1", "val3": "v3", "val5": "v5"},
			input:                "val6={val6},val1={val1},val2={val2},val3={val3},val4={val4},val5={val5},val6={val6}",
			expectedOutput:       "",
			expectedReplacedFlag: false,
			expectedErrMsg:       "{val2}, {val4}, {val6}",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			replacer := New(testCase.placeholders)
			actualValue, actualReplacedFlag, err := replacer.Process(testCase.input)
			if testCase.expectedErrMsg != "" {
				assert.Error(t, err)
				assert.ErrorContains(t, err, testCase.expectedErrMsg)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, testCase.expectedOutput, actualValue)
				assert.Equal(t, testCase.expectedReplacedFlag, actualReplacedFlag)
			}
		})
	}
}

func TestReplacer_ProcessMap(t *testing.T) {
	tests := []struct {
		name                 string
		placeholders         map[string]string
		input                map[string]string
		expectedOutput       map[string]string
		expectedReplacedFlag bool
		expectedErrMsg1      string
		expectedErrMsg2      string
	}{
		{
			name:                 "All placeholders in all values replaced",
			placeholders:         map[string]string{"val1": "v1", "val2": "v2"},
			input:                map[string]string{"key1": "val1={val1}", "key2": "val2={val2}", "key3": "val3=v3"},
			expectedOutput:       map[string]string{"key1": "val1=v1", "key2": "val2=v2", "key3": "val3=v3"},
			expectedReplacedFlag: true,
			expectedErrMsg1:      "",
			expectedErrMsg2:      "",
		},
		{
			name:                 "No placeholders, no changes",
			placeholders:         map[string]string{},
			input:                map[string]string{"key1": "val1=v1", "key2": "val2=v2", "key3": "val3=v3"},
			expectedOutput:       map[string]string{"key1": "val1=v1", "key2": "val2=v2", "key3": "val3=v3"},
			expectedReplacedFlag: false,
			expectedErrMsg1:      "",
			expectedErrMsg2:      "",
		},
		{
			name:                 "Missing placeholder value returns error msg with key, value and missing placeholders",
			placeholders:         map[string]string{"val1": "v1"},
			input:                map[string]string{"key1": "val1={val1}", "key2": "val2={missing1}{missing2}"},
			expectedOutput:       nil,
			expectedReplacedFlag: false,
			expectedErrMsg1:      "{missing1}, {missing2}",
			expectedErrMsg2:      "key=key2, value=val2={missing1}{missing2}",
		},
		{
			name:                 "empty map is ok",
			placeholders:         map[string]string{},
			input:                map[string]string{},
			expectedOutput:       map[string]string{},
			expectedReplacedFlag: false,
			expectedErrMsg1:      "",
			expectedErrMsg2:      "",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			replacer := New(testCase.placeholders)
			actualValue, actualReplacedFlag, err := replacer.ProcessMap(testCase.input)
			if testCase.expectedErrMsg1 != "" || testCase.expectedErrMsg2 != "" {
				assert.Error(t, err)
				if testCase.expectedErrMsg1 != "" {
					assert.ErrorContains(t, err, testCase.expectedErrMsg1)
				}
				if testCase.expectedErrMsg2 != "" {
					assert.ErrorContains(t, err, testCase.expectedErrMsg2)
				}
			} else {
				assert.NoError(t, err)
				assert.Equal(t, testCase.expectedOutput, actualValue)
				assert.Equal(t, testCase.expectedReplacedFlag, actualReplacedFlag)
			}
		})
	}
}
