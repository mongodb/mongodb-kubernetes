package om

// Paginated is the general interface for a single page returned by Ops Manager api.
type Paginated interface {
	HasNext() bool
	Results() []interface{}
}

type OMPaginaged struct {
	TotalCount int     `json:"totalCount"`
	Links      []*Link `json:"links,omitempty"`
	Next       *int    `json:"next,omitempty"`
}

type Link struct {
	Rel string `json:"rel"`
}

// HasNext return true if there is next page (see 'ApiBaseResource.handlePaginationInternal` in mms code)
func (o OMPaginaged) HasNext() bool {
	for _, l := range o.Links {
		if l.Rel == "next" {
			return true
		}
	}
	return false
}

// PageReader is the function that reads a single page by its number
type PageReader func(pageNum int) (Paginated, error)

// PageItemPredicate is the function that processes single item on the page and returns true if no further processing
// needs to be done (usually it's the search logic)
type PageItemPredicate func(interface{}) bool

// TraversePages reads page after page using 'apiFunc' and applies the 'predicate' for each item on the page.
// Stops traversal when the 'predicate' returns true
// Note, that in OM 4.0 the max number of pages is 100, but in OM 4.1 and CM - 500.
// So we'll traverse 100000 (200 pages 500 items on each) records in Cloud Manager and 20000 records in OM 4.0 - I believe it's ok
// This won't be necessary if MMS-5638 is implemented or if we make 'orgId' configuration mandatory
func TraversePages(apiFunc PageReader, predicate PageItemPredicate) (bool, error) {
	for i := 1; i < 200; i++ {
		paginated, e := apiFunc(i)
		if e != nil {
			return false, e
		}
		for _, entity := range paginated.Results() {
			if predicate(entity) {
				return true, nil
			}
		}
		if !paginated.HasNext() {
			return false, nil
		}
	}
	return false, nil
}
