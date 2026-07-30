// Package crema provides a probabilistic cache with revalidation and loaders.
//
// The cache can deduplicate concurrent loads via singleflight. Use WithMaxLoadTimeout
// to cap the execution time of singleflight loaders. When WithDirectLoader is used,
// the max load timeout is ignored and loaders run with the caller context.
//
// By default a still-valid cached value is served when a revalidation load
// fails, instead of propagating the loader error. Use
// WithRevalidationFallback(false) to always propagate loader errors.
package crema
