# plot-revalidation

Generates an SVG plot of the revalidation probability curves
$`p(t)=1-e^{-k(w-t)}`$, where `t` is the remaining TTL and `w` the revalidation
window. Each curve is `0` at its own window boundary and approaches `0.999` as
expiry nears.

## Usage

```sh
go run ./cmd/plot-revalidation
```

The command writes `revalidation.svg` to the current working directory. To
refresh the image used by the top-level README, move it to `doc/`:

```sh
go run ./cmd/plot-revalidation && mv revalidation.svg doc/revalidation.svg
```
