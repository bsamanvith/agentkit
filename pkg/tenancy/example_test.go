package tenancy_test

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// notes is a stand-in for a store method: it refuses a context with no tenant
// and keeps each tenant's data apart.
func notes(ctx context.Context) (string, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return "", err
	}
	return "notes of " + t.ID(), nil
}

// Middleware sets the tenant once; everything below reads it from the context.
func Example() {
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx := tenancy.With(context.Background(), acme)

	got, _ := notes(ctx)
	fmt.Println(got)

	_, err = notes(context.Background())
	fmt.Println("no tenant:", errors.Is(err, tenancy.ErrNoTenant))

	_, err = tenancy.NewTenant("../etc")
	fmt.Println("bad id:", errors.Is(err, tenancy.ErrInvalidTenant))
	// Output:
	// notes of acme
	// no tenant: true
	// bad id: true
}
