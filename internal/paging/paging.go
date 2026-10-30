// Package paging provides a generic paginated result.
package paging

type Page[T any] struct {
	Items   []T
	Total   int
	Page    int
	PerPage int
}

func (p Page[T]) Pages() int {
	if p.PerPage == 0 {
		return 0
	}
	return (p.Total + p.PerPage - 1) / p.PerPage
}
func (p Page[T]) HasPrev() bool { return p.Page > 1 }
func (p Page[T]) HasNext() bool { return p.Page < p.Pages() }
func (p Page[T]) Prev() int     { return p.Page - 1 }
func (p Page[T]) Next() int     { return p.Page + 1 }

// Normalize clamps page and perPage to sane values and returns the SQL offset.
func Normalize(page, perPage, def int) (int, int, int) {
	if perPage <= 0 || perPage > 100 {
		perPage = def
	}
	if page < 1 {
		page = 1
	}
	return page, perPage, (page - 1) * perPage
}
