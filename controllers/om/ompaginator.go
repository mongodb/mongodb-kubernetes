package om

import (
	"context"
	"errors"
	"time"

	"golang.org/x/xerrors"
)

const (
	// maxPages is the hard upper bound on the number of pages TraversePages reads.
	maxPages = 100

	// traversePagesTimeout is the overall deadline for a single TraversePages call.
	traversePagesTimeout = time.Minute
)

// ErrPageLimitExceeded is returned by TraversePages when Ops Manager keeps reporting a next page after maxPages pages.
var ErrPageLimitExceeded = errors.New("pagination exceeded the maximum number of pages")

// Paginated is the general interface for a single page returned by Ops Manager api.
type Paginated interface {
	HasNext() bool
	Results() []interface{}
}

type OMPaginated struct {
	TotalCount int     `json:"totalCount"`
	Links      []*Link `json:"links,omitempty"`
}

type Link struct {
	Rel string `json:"rel"`
}

// HasNext return true if there is next page (see 'ApiBaseResource.handlePaginationInternal' in mms code)
func (o OMPaginated) HasNext() bool {
	for _, l := range o.Links {
		if l.Rel == "next" {
			return true
		}
	}
	return false
}

// PageReader is the function that reads a single page by its number. The context carries the deadline of the whole
// traversal and must be propagated to the underlying HTTP request.
type PageReader func(ctx context.Context, pageNum int) (Paginated, error)

// PageItemPredicate is the function that processes single item on the page and returns true if no further processing
// needs to be done (usually it's the search logic)
type PageItemPredicate func(interface{}) bool

// TraversePages reads page after page using 'reader' and applies the 'predicate' for each item on the page.
// Stops traversal when the 'predicate' returns true or when the page has no 'next' link.
// The traversal is bounded both by a maximum number of pages and by an overall timeout: an error is returned when
// either bound is hit.
func TraversePages(ctx context.Context, reader PageReader, predicate PageItemPredicate) (found bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, traversePagesTimeout)
	defer cancel()

	paginated, err := readPage(ctx, reader, 1)
	if err != nil {
		return false, err
	}
	if applyPredicate(paginated, predicate) {
		return true, nil
	}

	// Note that we start from 2nd page as we've checked the 1st one above
	for pageNum := 2; paginated.HasNext(); pageNum++ {
		if pageNum > maxPages {
			return false, xerrors.Errorf("%w: stopped after %d pages", ErrPageLimitExceeded, maxPages)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, xerrors.Errorf("timed out traversing Ops Manager pages after %d pages: %w", pageNum-1, ctxErr)
		}

		paginated, err = readPage(ctx, reader, pageNum)
		if err != nil {
			return false, err
		}
		if applyPredicate(paginated, predicate) {
			return true, nil
		}
	}
	return false, nil
}

// readPage reads a single page.
func readPage(ctx context.Context, reader PageReader, pageNum int) (Paginated, error) {
	paginated, err := reader(ctx, pageNum)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// the OM client wraps requests in apierror.Error which drops the error chain,
			// so the context error is wrapped here to keep the deadline detectable
			return nil, xerrors.Errorf("failed reading Ops Manager page %d: %w", pageNum, ctxErr)
		}
		return nil, err
	}
	if paginated == nil {
		return nil, xerrors.Errorf("reader returned no data for Ops Manager page %d", pageNum)
	}
	return paginated, nil
}

func applyPredicate(paginated Paginated, predicate PageItemPredicate) bool {
	for _, entity := range paginated.Results() {
		if predicate(entity) {
			return true
		}
	}
	return false
}
