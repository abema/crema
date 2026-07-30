// Package gomemcache provides a Memcached-backed cache provider for crema.
//
// Note: the gomemcache client API does not accept contexts, so Memcached
// operations through this provider do not respect context cancellation or
// deadlines. When core singleflight is used, loads are detached in a
// goroutine, so Memcached access continues even if the caller's context ends.
//
// Memcached treats TTLs over 30 days as absolute timestamps. TTLs beyond the
// range supported by gomemcache may be shortened; see README.md.
package gomemcache
