package om

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPagination_SinglePage(t *testing.T) {
	ctx := context.Background()
	found, err := TraversePages(ctx, singleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "test" })
	assert.True(t, found)
	assert.NoError(t, err)

	found, err = TraversePages(ctx, singleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "fake" })
	assert.False(t, found)
	assert.NoError(t, err)
}

func TestPagination_MultiplePages(t *testing.T) {
	ctx := context.Background()

	pagesRead := 0
	reader := func(ctx context.Context, pageNum int) (Paginated, error) {
		pagesRead++
		return multipleOrganizationsPage(ctx, pageNum)
	}

	found, err := TraversePages(ctx, reader, func(obj interface{}) bool { return obj.(*Organization).Name == "test1220" })
	assert.True(t, found)
	assert.NoError(t, err)
	assert.Equal(t, 3, pagesRead)

	pagesRead = 0
	found, err = TraversePages(ctx, reader, func(obj interface{}) bool { return obj.(*Organization).Name == "test1400" })
	assert.False(t, found)
	assert.NoError(t, err)
	assert.Equal(t, 3, pagesRead)
}

func TestPagination_Error(t *testing.T) {
	_, err := TraversePages(context.Background(), func(_ context.Context, _ int) (Paginated, error) { return nil, errors.New("Error!") },
		func(obj interface{}) bool { return obj.(*Organization).Name == "test1220" })
	assert.EqualError(t, err, "Error!")
}

func TestPagination_StopsAtMaxPagesWhenServerAlwaysReportsNext(t *testing.T) {
	pagesRead := 0
	reader := func(_ context.Context, pageNum int) (Paginated, error) {
		pagesRead++
		return AutomationAgentStatusResponse{
			OMPaginated:      OMPaginated{TotalCount: math.MaxInt64, Links: []*Link{{Rel: "next"}}},
			AutomationAgents: []AgentStatus{{Hostname: fmt.Sprintf("host-%d", pageNum), TypeName: "AUTOMATION"}},
		}, nil
	}

	itemsSeen := 0
	found, err := TraversePages(context.Background(), reader, func(interface{}) bool {
		itemsSeen++
		return false
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPageLimitExceeded)
	assert.False(t, found)
	assert.Equal(t, maxPages, pagesRead, "the reader must be called exactly maxPages times")
	assert.Equal(t, maxPages, itemsSeen)
}

func TestPagination_ContextDeadlineAbortsTraversal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	pagesRead := 0
	reader := func(ctx context.Context, pageNum int) (Paginated, error) {
		pagesRead++
		if pageNum == 1 {
			return endlessOrganizationsPage(), nil
		}
		// Emulate an Ops Manager that never answers.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	_, err := TraversePages(ctx, reader, func(interface{}) bool { return false })

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.LessOrEqual(t, pagesRead, 2, "no page may be requested once the deadline has passed")
}

func TestPagination_DeadlineKeepsReaderError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	reader := func(ctx context.Context, _ int) (Paginated, error) {
		<-ctx.Done()
		return nil, errors.New("401 Unauthorized")
	}

	_, err := TraversePages(ctx, reader, func(interface{}) bool { return false })

	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "401 Unauthorized")
}

func TestPagination_ReaderReceivesContextWithDeadline(t *testing.T) {
	start := time.Now()
	var deadline time.Time
	var hasDeadline bool
	reader := func(ctx context.Context, pageNum int) (Paginated, error) {
		deadline, hasDeadline = ctx.Deadline()
		return singleOrganizationsPage(ctx, pageNum)
	}

	_, err := TraversePages(context.Background(), reader, func(interface{}) bool { return false })

	require.NoError(t, err)
	assert.True(t, hasDeadline, "the reader must receive a context bounded by traversePagesTimeout")
	assert.WithinDuration(t, start.Add(traversePagesTimeout), deadline, 5*time.Second)
}

func TestPagination_CancelledParentContextStopsBeforeNextPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pagesRead := 0
	reader := func(_ context.Context, _ int) (Paginated, error) {
		pagesRead++
		// Cancel while processing the first page: the traversal must not request page 2.
		cancel()
		return endlessOrganizationsPage(), nil
	}

	_, err := TraversePages(ctx, reader, func(interface{}) bool { return false })

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, pagesRead)
}

func singleOrganizationsPage(_ context.Context, pageNum int) (Paginated, error) {
	if pageNum == 1 {
		// Note, that we don't specify 'next' attribute, so no extra pages will be requested
		return &OrganizationsResponse{
			OMPaginated:   OMPaginated{TotalCount: 1},
			Organizations: []*Organization{{ID: "1323", Name: "test"}},
		}, nil
	}
	return nil, errors.New("Not found!")
}

// endlessOrganizationsPage is a page that always claims there is a next one.
func endlessOrganizationsPage() Paginated {
	return &OrganizationsResponse{
		OMPaginated:   OMPaginated{TotalCount: math.MaxInt64, Links: []*Link{{Rel: "next"}}},
		Organizations: generateOrganizations(0, 1),
	}
}

// multipleOrganizationsPage serves 1300 organizations over 3 pages.
func multipleOrganizationsPage(_ context.Context, pageNum int) (Paginated, error) {
	switch pageNum {
	case 1:
		return &OrganizationsResponse{
			OMPaginated:   OMPaginated{TotalCount: 1300, Links: []*Link{{Rel: "next"}}},
			Organizations: generateOrganizations(0, 500),
		}, nil
	case 2:
		return &OrganizationsResponse{
			OMPaginated:   OMPaginated{TotalCount: 1300, Links: []*Link{{Rel: "next"}}},
			Organizations: generateOrganizations(500, 500),
		}, nil
	case 3:
		return &OrganizationsResponse{
			OMPaginated:   OMPaginated{TotalCount: 1300},
			Organizations: generateOrganizations(1000, 300),
		}, nil
	}
	return nil, errors.New("Not found!")
}

func generateOrganizations(startFrom, count int) []*Organization {
	ans := make([]*Organization, count)
	c := startFrom
	for i := 0; i < count; i++ {
		ans[i] = &Organization{ID: fmt.Sprintf("id%d", c), Name: fmt.Sprintf("test%d", c)}
		c++
	}
	return ans
}
