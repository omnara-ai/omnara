#!/usr/bin/env bash

set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: sign-notarize-omnarad.sh RELEASE_DIRECTORY" >&2
  exit 1
fi

for name in \
  RUNNER_TEMP \
  MACOS_CERT_P12 \
  MACOS_CERT_PASSWORD \
  MACOS_SIGNING_IDENTITY \
  APPLE_API_KEY \
  APPLE_API_KEY_ID \
  APPLE_API_ISSUER_ID; do
  if [[ -z "${!name:-}" ]]; then
    echo "$name is required" >&2
    exit 1
  fi
done

release_directory=$1
artifacts=(
  "$release_directory/omnarad-darwin-amd64"
  "$release_directory/omnarad-darwin-arm64"
)
for artifact in "${artifacts[@]}"; do
  if [[ ! -f "$artifact" || -L "$artifact" ]]; then
    echo "$artifact must be a regular file" >&2
    exit 1
  fi
done

certificate_path="$RUNNER_TEMP/omnarad-signing.p12"
keychain_path="$RUNNER_TEMP/omnarad-signing.keychain-db"
api_key_path="$RUNNER_TEMP/AuthKey_${APPLE_API_KEY_ID}.p8"
notarization_directory="$RUNNER_TEMP/omnarad-notarization"
archive_path="$RUNNER_TEMP/omnarad-notarization.zip"
response_path="$RUNNER_TEMP/omnarad-notarization.json"
log_path="$RUNNER_TEMP/omnarad-notarization-log.json"
keychain_password=$(/usr/bin/uuidgen)

cleanup() {
  security delete-keychain "$keychain_path" >/dev/null 2>&1 || true
  rm -f \
    "$certificate_path" \
    "$api_key_path" \
    "$archive_path" \
    "$response_path" \
    "$log_path" \
    "$notarization_directory/omnarad-darwin-amd64" \
    "$notarization_directory/omnarad-darwin-arm64"
  rmdir "$notarization_directory" >/dev/null 2>&1 || true
}
trap cleanup EXIT

umask 077
printf '%s' "$MACOS_CERT_P12" | /usr/bin/base64 -D >"$certificate_path"
printf '%s' "$APPLE_API_KEY" | /usr/bin/base64 -D >"$api_key_path"

security create-keychain -p "$keychain_password" "$keychain_path"
security unlock-keychain -p "$keychain_password" "$keychain_path"
security set-keychain-settings -lut 21600 "$keychain_path"
security import "$certificate_path" \
  -k "$keychain_path" \
  -P "$MACOS_CERT_PASSWORD" \
  -T /usr/bin/codesign
security set-key-partition-list \
  -S apple-tool:,apple: \
  -k "$keychain_password" \
  "$keychain_path"
security list-keychains -d user -s "$keychain_path"

for artifact in "${artifacts[@]}"; do
  chmod 0755 "$artifact"
  codesign \
    --force \
    --options runtime \
    --timestamp \
    --identifier com.omnara.omnarad \
    --sign "$MACOS_SIGNING_IDENTITY" \
    --keychain "$keychain_path" \
    "$artifact"
  codesign --verify --strict --verbose=2 "$artifact"
done

mkdir "$notarization_directory"
cp "${artifacts[@]}" "$notarization_directory/"
ditto -c -k --sequesterRsrc "$notarization_directory" "$archive_path"

if ! xcrun notarytool submit "$archive_path" \
  --key "$api_key_path" \
  --key-id "$APPLE_API_KEY_ID" \
  --issuer "$APPLE_API_ISSUER_ID" \
  --wait \
  --output-format json | tee "$response_path"; then
  submission_id=$(jq -r '.id // empty' "$response_path")
  if [[ -n "$submission_id" ]]; then
    xcrun notarytool log "$submission_id" \
      --key "$api_key_path" \
      --key-id "$APPLE_API_KEY_ID" \
      --issuer "$APPLE_API_ISSUER_ID" | tee "$log_path" || true
  fi
  exit 1
fi

status=$(jq -r '.status // empty' "$response_path")
if [[ "$status" != "Accepted" ]]; then
  submission_id=$(jq -r '.id // empty' "$response_path")
  if [[ -n "$submission_id" ]]; then
    xcrun notarytool log "$submission_id" \
      --key "$api_key_path" \
      --key-id "$APPLE_API_KEY_ID" \
      --issuer "$APPLE_API_ISSUER_ID" | tee "$log_path" || true
  fi
  echo "notarization status is $status" >&2
  exit 1
fi

for artifact in "${artifacts[@]}"; do
  codesign --verify --strict --verbose=2 "$artifact"
  codesign -vvvv -R=notarized --check-notarization "$artifact"
done
