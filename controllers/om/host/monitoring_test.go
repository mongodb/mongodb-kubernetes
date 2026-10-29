package host

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
)

type fakeGetRemover struct {
	hosts     []Host
	removeErr func(remaining int) error
}

func (f *fakeGetRemover) GetHosts(_ context.Context) (*Result, error) {
	return &Result{Results: append([]Host(nil), f.hosts...)}, nil
}

func (f *fakeGetRemover) RemoveHost(_ context.Context, hostID string) error {
	if f.removeErr != nil {
		if err := f.removeErr(len(f.hosts)); err != nil {
			return err
		}
	}
	f.hosts = slices.DeleteFunc(f.hosts, func(h Host) bool { return h.Id == hostID })
	return nil
}

func newFake(removeErr func(remaining int) error) *fakeGetRemover {
	return &fakeGetRemover{
		hosts:     []Host{{Id: "1", Hostname: "a"}, {Id: "2", Hostname: "b"}, {Id: "3", Hostname: "c"}},
		removeErr: removeErr,
	}
}

func rejectLastHost(remaining int) error {
	if remaining == 1 {
		return apierror.NewErrorWithCode(apierror.CannotDeleteHostBackupEnabled)
	}
	return nil
}

var allHostnames = []string{"a", "b", "c"}

func TestStopMonitoring_ToleratesBackupRejection(t *testing.T) {
	f := newFake(rejectLastHost)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, true, zap.S()))
	assert.Len(t, f.hosts, 1, "Ops Manager keeps only the last host to anchor the backup")
}

func TestStopMonitoring_ReturnsOtherErrors(t *testing.T) {
	status := 500
	f := newFake(func(int) error { return &apierror.Error{Status: &status, ErrorCode: "UNEXPECTED_ERROR"} })

	err := StopMonitoring(context.Background(), f, allHostnames, true, zap.S())
	require.Error(t, err)
	apiErr, ok := errors.AsType[*apierror.Error](err)
	require.True(t, ok)
	assert.Equal(t, "UNEXPECTED_ERROR", apiErr.ErrorCode)
}

func TestStopMonitoring_ReturnsBackupRejection(t *testing.T) {
	f := newFake(rejectLastHost)

	err := StopMonitoring(context.Background(), f, allHostnames, false, zap.S())
	require.Error(t, err)
	apiErr, ok := errors.AsType[*apierror.Error](err)
	require.True(t, ok)
	assert.Equal(t, apierror.CannotDeleteHostBackupEnabled, apiErr.ErrorCode)
	assert.Len(t, f.hosts, 1, "other hosts are still removed")
}
