// Package crema provides a probabilistic cache with revalidation and loaders.
//
// The cache can deduplicate concurrent loads via singleflight. Use WithMaxLoadTimeout
// to cap the execution time of singleflight loaders. When WithDirectLoader is used,
// the max load timeout is ignored and loaders run with the caller context.
//
// By default a still-valid cached value is served when a revalidation load
// fails, instead of propagating the loader error. Use
// WithRevalidationFallback(false) to always propagate loader errors.
//
// Load results are not cached by default. WithNegativeCacheProvider caches
// results that mean a value is absent, while WithLoadErrorCacheProvider caches
// selected errors that do not mean absence. Both use an independent TTL.
package crema
