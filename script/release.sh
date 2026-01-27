#!/bin/bash
set -eu -o pipefail

MODULE_DIRS=(
  "."
  "example"
  "ext/go-json"
  "ext/golang-lru"
  "ext/gomemcache"
  "ext/protobuf"
  "ext/ristretto"
  "ext/valkey-go"
)

usage() {
  echo "Usage: $(basename "$0") <version>"
  echo "  version: release version (e.g. v1.2.3)"
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

pushd "$(dirname "$0")/.." > /dev/null # enter root

for dir in "${MODULE_DIRS[@]}" ; do
  echo "### module ${dir} ###"
  create_tag "${dir}" "${VERSION}"
  echo ""
done
echo "Finish push tags. Create Release..."

gh release create "${VERSION}" --generate-notes

echo "Release ${VERSION} successflly."

popd > /dev/null # exit root
