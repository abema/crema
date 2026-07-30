// Package crema provides a probabilistic cache with revalidation and loaders.
//
// The cache can deduplicate concurrent loads via singleflight. Use WithMaxLoadTimeout
// to cap the execution time of loaders. The cap applies to both the singleflight
// loader and the direct loader selected by WithDirectLoader, regardless of the order
// the options are given in. Singleflight loaders run with a context detached from the
// caller, while direct loaders run with the caller context.
package crema
