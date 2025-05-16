package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/xerrors"
)

type mockHttpGetter struct {
	resp *http.Response
	err  error
}

func (m *mockHttpGetter) Get(string) (*http.Response, error) {
	return m.resp, m.err
}

// newMockHttpGetter returns a httpGetter which will return the given status code, the body and error provided.
func newMockHttpGetter(code int, body []byte, err error) *mockHttpGetter {
	return &mockHttpGetter{
		resp: &http.Response{
			StatusCode: code,
			Body:       io.NopCloser(bytes.NewReader(body)),
		},
		err: err,
	}
}

func healthStatusResponseToBytes(hr HealthResponse) []byte {
	bytes, err := json.Marshal(hr)
	if err != nil {
		panic(err)
	}
	return bytes
}

func TestCheckHealthEndpoint(t *testing.T) {
	t.Run("Test 200 Code with invalid body fails", func(t *testing.T) {
		m := newMockHttpGetter(200, healthStatusResponseToBytes(HealthResponse{
			SyncDb:   "OK",
			BackupDb: "OK",
			MmsDb:    "no master",
		}), nil)
		code := checkHealthEndpoint(m)
		assert.Equal(t, 1, code)
	})

	t.Run("Test 200 Code with valid body succeeds", func(t *testing.T) {
		m := newMockHttpGetter(200, healthStatusResponseToBytes(HealthResponse{
			SyncDb:   "OK",
			BackupDb: "OK",
			MmsDb:    "OK",
		}), nil)
		code := checkHealthEndpoint(m)
		assert.Equal(t, 0, code)
	})

	t.Run("Test non-200 Code fails", func(t *testing.T) {
		m := newMockHttpGetter(300, healthStatusResponseToBytes(HealthResponse{
			SyncDb:   "OK",
			BackupDb: "OK",
			MmsDb:    "OK",
		}), nil)
		code := checkHealthEndpoint(m)
		assert.Equal(t, 1, code)
	})

	t.Run("Test error from http client fails", func(t *testing.T) {
		m := newMockHttpGetter(200, nil, xerrors.Errorf("error"))
		code := checkHealthEndpoint(m)
		assert.Equal(t, 1, code)
	})
}
