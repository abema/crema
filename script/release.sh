#!/bin/bash
set -eu -o pipefail

MODULE_PREFIX="https://github.com/abema/crema"

SUBMODULE_DIRS=(
  "ext/go-json"
  "ext/golang-lru"
  "ext/gomemcache"
  "ext/protobuf"
  "ext/ristretto"
  "ext/valkey-go"
  "example"
)

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

# Release core module
echo ""
echo "### release core module ###"
create_tag "." "${VERSION}"

# Update submodules
echo ""
echo "### update submodules ###"
for dir1 in "${MODULE_DIRS[@]}" ; do
  pushd ${dir1} > /dev/null
    echo "update ${dir1}/go.mod"
    go get "${MODULE_PREFIX}@${VERSION}"
  popd
done

# Commit Update
echo ""
echo "### commit update ###"
git commit -a -m "update submodules to ${VERSION}"
git push origin main

# Release submodules
echo "### release submodules ###"
for dir1 in "${MODULE_DIRS[@]}" ; do
  echo "release ${dir1}"
  create_tag "${dir1}" "${VERSION}"
done

echo "### update submodule references ###"
for dir1 in "${MODULE_DIRS[@]}" ; do
  pushd ${dir1} > /dev/null
    echo "update ${dir1}"
    for dir2 in "${MODULE_DIRS[@]}" ; do
      go get "${MODULE_PREFIX}/${dir2}@${VERSION}"
      go mod tidy
    done
  popd
done

echo ""
echo "Create GitHub Release..."

gh release create "${VERSION}" --generate-notes

echo "Release ${VERSION} successflly."

popd > /dev/null # exit root
