package common

import (
	"context"
	"fmt"
)

// ClampLimit returns n, or def when n is zero, and an error that matches
// [tool.ErrInvalidInput] (through [Invalid]) when n is negative or over max.
func ClampLimit(name string, n, def, max int) (int, error) {
	switch {
	case n == 0:
		return def, nil
	case n < 0 || n > max:
		return 0, Invalid("%s %d: want 1..%d", name, n, max)
	}
	return n, nil
}

// maxPages bounds [Paginate] whatever the API returns, so a server that
// always answers with a next-page token cannot hold a call forever.
const maxPages = 50

// Paginate calls fetch with a page token ("" for the first page) until it has
// limit items or there is no next page. fetch is asked for at most want items
// (limit minus what is collected), so it can set the API's page size. It
// returns the items, at most limit, and the token to continue from: "" when
// the results ended. fetch must not return more than want items (it passes
// want as the page size); if it does, the surplus is dropped.
func Paginate[T any](ctx context.Context, limit int, start string, fetch func(ctx context.Context, token string, want int) (items []T, next string, err error)) ([]T, string, error) {
	if limit < 1 {
		return nil, "", fmt.Errorf("paginate: limit %d", limit)
	}
	var out []T
	token := start
	for range maxPages {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		items, next, err := fetch(ctx, token, limit-len(out))
		if err != nil {
			return nil, "", err
		}
		if room := limit - len(out); len(items) > room {
			items = items[:room]
		}
		out = append(out, items...)
		if next == "" || len(out) == limit {
			return out, next, nil
		}
		token = next
	}
	return out, token, nil
}
