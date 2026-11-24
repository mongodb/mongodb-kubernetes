package host

import (
	"context"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

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

// changingGetRemover returns a different host list on each read, to model a
// host that registers after StopMonitoring's first read.
type changingGetRemover struct {
	reads  [][]Host
	read   int
	remove func(hostID string) error
}

func (f *changingGetRemover) GetHosts(_ context.Context) (*Result, error) {
	hosts := f.reads[min(f.read, len(f.reads)-1)]
	f.read++
	return &Result{Results: append([]Host(nil), hosts...)}, nil
}

func (f *changingGetRemover) RemoveHost(_ context.Context, hostID string) error {
	if f.remove != nil {
		return f.remove(hostID)
	}
	return apierror.NewErrorWithCode(apierror.CannotDeleteHostBackupEnabled)
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

// Ops Manager reports its refusal as a generic 500, so the remaining hosts
// must both match the expected count and carry a backup-anchor refusal.
func TestStopMonitoring_AcceptsTheExpectedRemainingHosts(t *testing.T) {
	f := newFake(rejectLastHost)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 1, zap.S()))
	require.Len(t, f.hosts, 1)
	assert.Equal(t, "c", f.hosts[0].Hostname)
}

// Ops Manager answers the anchor refusal with a generic 500 today.
func TestStopMonitoring_AcceptsTheGenericServerErrorRefusal(t *testing.T) {
	status := http.StatusInternalServerError
	f := newFake(func(remaining int, _ string) error {
		if remaining == 1 {
			return &apierror.Error{Status: &status, Detail: "Unexpected error."}
		}
		return nil
	})

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 1, zap.S()))
	assert.Len(t, f.hosts, 1)
}

// A permission error must fail even when the count matches. The count alone
// cannot prove that Ops Manager kept a backup anchor.
func TestStopMonitoring_RejectsUnrelatedErrorsWithinTheAllowance(t *testing.T) {
	status := http.StatusForbidden
	f := newFake(func(remaining int, _ string) error {
		if remaining == 1 {
			return &apierror.Error{Status: &status, Detail: "forbidden"}
		}
		return nil
	})

	err := StopMonitoring(context.Background(), f, allHostnames, 1, zap.S())
	require.Error(t, err)
	assert.Len(t, f.hosts, 1)
	assert.Contains(t, err.Error(), "c")
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
	assert.Contains(t, err.Error(), "a")
	assert.Contains(t, err.Error(), "b")
}

func TestStopMonitoring_ReturnsErrorWhenNoHostMayRemain(t *testing.T) {
	f := newFake(rejectLastHost)

	err := StopMonitoring(context.Background(), f, allHostnames, 0, zap.S())
	require.Error(t, err)
	assert.Len(t, f.hosts, 1)
	assert.Contains(t, err.Error(), "c")
}

// A host can register between the first read and the verification read.
func TestStopMonitoring_CountsAHostThatAppearsAfterTheFirstRead(t *testing.T) {
	f := &changingGetRemover{
		reads: [][]Host{
			{{Id: "1", Hostname: "a"}},
			{{Id: "1", Hostname: "a"}, {Id: "2", Hostname: "b"}},
		},
	}

	err := StopMonitoring(context.Background(), f, []string{"a", "b"}, 0, zap.S())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a")
	assert.Contains(t, err.Error(), "b")
}

// The accepted keep-backup case names the host that anchors the backup.
func TestStopMonitoring_WarnsWithTheLeftoverHostnames(t *testing.T) {
	core, observed := observer.New(zap.WarnLevel)
	f := newFake(rejectLastHost)

	require.NoError(t, StopMonitoring(context.Background(), f, allHostnames, 1, zap.New(core).Sugar()))

	entries := observed.FilterMessageSnippet("still monitored").All()
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Message, "c")
}
