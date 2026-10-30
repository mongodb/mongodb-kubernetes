package host

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	"github.com/mongodb/mongodb-kubernetes/controllers/om/apierror"
)

type fakeGetRemover struct {
	hosts     []Host
	removeErr func(remaining int, hostID string) error
}

func (f *fakeGetRemover) GetHosts(_ context.Context) (*Result, error) {
	return &Result{Results: append([]Host(nil), f.hosts...)}, nil
}

func (f *fakeGetRemover) RemoveHost(_ context.Context, hostID string) error {
	if f.removeErr != nil {
		if err := f.removeErr(len(f.hosts), hostID); err != nil {
			return err
		}
	}
	f.hosts = slices.DeleteFunc(f.hosts, func(h Host) bool { return h.Id == hostID })
	return nil
}

func newFake(removeErr func(remaining int, hostID string) error) *fakeGetRemover {
	return &fakeGetRemover{
		hosts:     []Host{{Id: "1", Hostname: "a"}, {Id: "2", Hostname: "b"}, {Id: "3", Hostname: "c"}},
		removeErr: removeErr,
	}
}

func rejectLastHost(remaining int, _ string) error {
	if remaining == 1 {
		return apierror.NewErrorWithCode(apierror.CannotDeleteHostBackupEnabled)
	}
	return nil
}

var allHostnames = []string{"a", "b", "c"}

func TestStopMonitoring_RemovesAllHosts(t *testing.T) {
	f := newFake(nil)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 0, zap.S()))
	assert.Empty(t, f.hosts)
}

// Ops Manager reports its refusal as a generic 500, so the remaining host count
// decides, not the error.
func TestStopMonitoring_AcceptsTheExpectedRemainingHosts(t *testing.T) {
	f := newFake(rejectLastHost)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 1, zap.S()))
	require.Len(t, f.hosts, 1)
	assert.Equal(t, "c", f.hosts[0].Hostname)
}

// A sharded cluster keeps one host per replica set.
func TestStopMonitoring_AcceptsOneAnchorPerReplicaSet(t *testing.T) {
	f := newFake(func(_ int, hostID string) error {
		if hostID == "2" || hostID == "3" {
			return apierror.NewErrorWithCode(apierror.CannotDeleteHostBackupEnabled)
		}
		return nil
	})

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 2, zap.S()))
	assert.Len(t, f.hosts, 2)
}

// Ops Manager can allow the removal of one replica set's anchor while it keeps
// another. Fewer than the maximum is still an acceptable end state.
func TestStopMonitoring_AcceptsFewerHostsThanTheMaximum(t *testing.T) {
	f := newFake(rejectLastHost)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 2, zap.S()))
	assert.Len(t, f.hosts, 1)
}

func TestStopMonitoring_ReturnsErrorWhenMoreHostsRemain(t *testing.T) {
	f := newFake(func(_ int, hostID string) error {
		if hostID != "3" {
			return apierror.NewErrorWithCode(apierror.CannotDeleteHostBackupEnabled)
		}
		return nil
	})

	err := StopMonitoring(context.Background(), f, allHostnames, 1, zap.S())
	require.Error(t, err)
	assert.Len(t, f.hosts, 2)
}

func TestStopMonitoring_ReturnsErrorWhenNoHostMayRemain(t *testing.T) {
	f := newFake(rejectLastHost)

	err := StopMonitoring(context.Background(), f, allHostnames, 0, zap.S())
	require.Error(t, err)
	assert.Len(t, f.hosts, 1)
}
