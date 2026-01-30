#!/bin/bash
set -eu -o pipefail

MODULE_PREFIX="github.com/abema/crema"

SUBMODULE_DIRS=(
  "ext/go-json"
  "ext/golang-lru"
  "ext/gomemcache"
  "ext/protobuf"
  "ext/rueidis"
  "ext/ristretto"
  "ext/valkey-go"
  "example"
)

export GOPRIVATE=${MODULE_PREFIX}

usage() {
  echo "Usage: $(basename "$0") <version>"
  echo "  version: release version (e.g. v1.2.3)"
}

ensure_clean() {
  if [ -n "$(git status --porcelain)" ]; then
    echo "ERROR: Working tree is dirty"
    exit 1
  fi
}

create_tag() {
  local dir="$1"
  local version="$2"
  local tag="${dir}/${version}"
  tag="${tag#./}"

  echo "create tag ${tag}"
  git tag -a "${tag}" -m "Release ${tag}"
  git push origin "${tag}"
}

release_tag() {
  local dir="$1"
  local version="$2"
  local latest="$3"
  local tag="${dir}/${version}"
  tag="${tag#./}"

  echo "release tag ${tag}"
  gh release create "${VERSION}" --generate-notes --latest="${latest}"
}

if [ $# -eq 0 ] || [ "$1" = "-h" ] || [ "$1" = "--help" ]; then
  usage
  exit 1
fi

VERSION="$1"

if ! [[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$ ]]; then
  echo "Error: version must be in the form v1.2.3 or v1.2.3-beta.2"
  usage
  exit 1
fi

read -r -p "Release version '${VERSION}'? Type 'yes' to continue: " CONFIRM
if [ "$CONFIRM" != "yes" ]; then
  echo "Aborted."
  exit 1
fi
echo "Releasing version ${VERSION}..."

ensure_clean

pushd "$(dirname "$0")/.." > /dev/null # enter root

# Create tag for core module
echo ""
echo "### create tag for core module ###"
create_tag "." "${VERSION}"

# Update submodules
echo ""
echo "### update submodules ###"
for dir in "${SUBMODULE_DIRS[@]}" ; do
  pushd "${dir}" > /dev/null
    echo "update ${dir}/go.mod"
    go get "${MODULE_PREFIX}@${VERSION}"
  popd > /dev/null
done
go work sync

# Commit Update
echo ""
echo "### commit update ###"
git commit -a -m "update submodules to ${VERSION}"
git push origin main

# Release submodules
echo "### release submodules ###"
for dir in "${SUBMODULE_DIRS[@]}" ; do
  echo "release ${dir}"
  create_tag "${dir}" "${VERSION}"
  release_tag "${dir}" "${VERSION}" "false"
done

echo "### update example references ###"
pushd "example" > /dev/null
  for dir in "${SUBMODULE_DIRS[@]}" ; do
    if [ "${dir}" = "example" ]; then
      continue
    fi
    go get "${MODULE_PREFIX}/${dir}@${VERSION}"
    go mod tidy
  done
popd > /dev/null
go work sync
git commit -a -m "update example to ${VERSION}"
git push origin main

echo ""
echo "Create GitHub Release..."

gh release create "${VERSION}" --generate-notes
release_tag "${dir}" "${VERSION}" "false"

echo "Release ${VERSION} successflly."

popd > /dev/null # exit root
