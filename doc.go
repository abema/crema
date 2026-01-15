// Package crema provides a probabilistic cache with revalidation and loaders.
//
// The cache can deduplicate concurrent loads via singleflight. Use WithMaxLoadTimeout
// to cap the execution time of singleflight loaders. When WithDirectLoader is used,
// the max load timeout is ignored and loaders run with the caller context.
package crema
