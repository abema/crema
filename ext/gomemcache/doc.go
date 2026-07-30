// Package gomemcache provides a Memcached-backed cache provider for crema.
//
// Note: the gomemcache client API does not accept contexts, so Memcached
// operations through this provider do not respect context cancellation or
// deadlines. When core singleflight is used, loads are detached in a
// goroutine, so Memcached access continues even if the caller's context ends.
//
// Note: Memcached reads an expiration above 30 days as an absolute UNIX
// timestamp, and that timestamp is a 32-bit value, so no expiry after
// 2038-01-19T03:14:07Z can be expressed. TTLs are mapped to the closest expiry
// Memcached can represent, which means a TTL longer than 30 days may silently
// end up shorter than requested. An entry is never stored already expired. See
// README.md for the exact rules.
package gomemcache
