// Package tenancy carries the tenant a request belongs to in a
// [context.Context]. Middleware sets it once at the edge ([With]); everything
// below reads it ([From], [Require]). The store scopes every read and write by
// it, so one tenant can never see another's data, and a context with no tenant
// is refused instead of treated as "all tenants".
//
// A [Tenant] can only be made by [NewTenant], which accepts identifiers that
// are safe to use as a database key, a SQL value, a URL segment or a metric
// label (but see [NewTenant] before using one as a file name). The same scope can carry a user id and a request id for audit logs.
// They are labels: this package does not authenticate anyone.
package tenancy
