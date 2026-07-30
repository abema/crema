# Changelog

Notable behavior changes to the core `github.com/abema/crema` module.

## Unreleased

### Changed

- Revalidation probability now grows as an entry approaches expiry instead of
  peaking at the start of the window. Reload timing may move later and remains
  dependent on request rate.
- `WithRevalidationWindow(duration)` now uses the configured duration exactly.
  The previous effective window was about `0.767 * duration`.

## v1.1.1 and earlier

See the [GitHub releases](https://github.com/abema/crema/releases).
