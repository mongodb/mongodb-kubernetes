package om

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPagination_SinglePage(t *testing.T) {
	found, err := TraversePages(singleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "test" })
	assert.True(t, found)
	assert.NoError(t, err)

	found, err = TraversePages(singleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "fake" })
	assert.False(t, found)
	assert.NoError(t, err)
}

func TestPagination_MultiplePages(t *testing.T) {
	found, err := TraversePages(multipleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "test1220" })
	assert.True(t, found)
	assert.NoError(t, err)
	assert.Equal(t, 3, numberOfPagesTraversed)

	found, err = TraversePages(multipleOrganizationsPage, func(obj interface{}) bool { return obj.(*Organization).Name == "test1400" })
	assert.False(t, found)
	assert.NoError(t, err)
}

func TestPagination_Error(t *testing.T) {
	_, err := TraversePages(func(pageNum int) (Paginated, error) { return nil, errors.New("Error!") },
		func(obj interface{}) bool { return obj.(*Organization).Name == "test1220" })
	assert.Errorf(t, err, "Error!")
}

var singleOrganizationsPage = func(pageNum int) (Paginated, error) {
	if pageNum == 1 {
		// Note, that we don't specify 'next' attribute, so no extra pages will be requested
		return &OrganizationsResponse{
			OMPaginated:   OMPaginated{TotalCount: 1},
			Organizations: []*Organization{{ID: "1323", Name: "test"}},
		}, nil
	}
	return nil, errors.New("Not found!")
}

var numberOfPagesTraversed = 0

var multipleOrganizationsPage = func(pageNum int) (Paginated, error) {
	numberOfPagesTraversed++
	// page 1
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
