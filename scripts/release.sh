#!/usr/bin/env bash
# Protected-main release flow: prepare the four release files on a dedicated
# branch, open/reuse one PR whose commit carries DCO sign-off, wait for
# protected auto-merge, tag the exact PR merge commit, then wait for the
# published release evidence.
#
# Usage:
#   scripts/release.sh patch|minor|major|<x.y.z>|<yy.mm.dd[.hh.mm]> [--no-edit]
#   scripts/release.sh <version>|now --prepare-only --no-edit
#   scripts/release.sh <version> --reviewed-head <full-sha> --no-edit
#   scripts/release.sh                            # report commits since tag

set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
# shellcheck disable=SC1091
source "$ROOT/scripts/release-version.sh"

NO_EDIT=0
PREPARE_ONLY=0
REVIEWED_HEAD=''
ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-edit) NO_EDIT=1; shift ;;
    --prepare-only) PREPARE_ONLY=1; shift ;;
    --reviewed-head)
      [[ "${2:-}" =~ ^[0-9a-f]{40}$ ]] || {
        echo 'error: --reviewed-head requires a full lowercase commit SHA' >&2
        exit 2
      }
      REVIEWED_HEAD="$2"; shift 2
      ;;
    --*) echo "error: unknown option: $1" >&2; exit 2 ;;
    *) ARGS+=("$1"); shift ;;
  esac
done
MODE="${ARGS[0]:-}"
if [[ ${#ARGS[@]} -gt 1 || ( "$PREPARE_ONLY" -eq 1 && -n "$REVIEWED_HEAD" ) ||
      ( -z "$MODE" && ( "$PREPARE_ONLY" -eq 1 || -n "$REVIEWED_HEAD" ) ) ]]; then
  echo 'error: use <version>|now --prepare-only, or <version> --reviewed-head <sha>' >&2
  exit 2
fi
if [[ -n "$REVIEWED_HEAD" ]] && ! release_version::is_supported "$MODE"; then
  echo 'error: reviewed resume requires the explicit prepared version' >&2
  exit 2
fi
case "${EDITOR:-}" in
  ""|true|:|cat|tee) NO_EDIT=1 ;;
esac
if [[ "${RELEASE_NO_EDIT:-}" == "1" ]]; then
  NO_EDIT=1
fi

MERGE_TIMEOUT="${RELEASE_MERGE_TIMEOUT:-1800}"
MERGE_POLL="${RELEASE_MERGE_POLL:-10}"
if ! [[ "$MERGE_TIMEOUT" =~ ^[0-9]+$ && "$MERGE_TIMEOUT" -gt 0 &&
        "$MERGE_POLL" =~ ^[0-9]+$ && "$MERGE_POLL" -gt 0 ]]; then
  echo "error: RELEASE_MERGE_TIMEOUT and RELEASE_MERGE_POLL must be positive integers" >&2
  exit 2
fi

EXPECTED_FILES=$'README.md\nVERSION\ndocs/CHANGELOG.md\ndocs/INSTALL.md'
EXTERNAL_STAGE_MANIFEST='backend/contracts/fixtures/external-stage/manifest-v1.json'
EXTERNAL_STAGE_MANIFEST_V2='backend/contracts/fixtures/external-stage-v2/manifest-v2.json'
RECOVERY_RECEIPT_DIR='scripts/release/recovery'
CALENDAR_RECOVERY_MERGE_OID=''
AUDITED_RELEASE_RECOVERY_MERGE_OID=''
AUDITED_RELEASE_RECOVERY_RECEIPT_OID=''
RELEASE_TAG_FETCH_BATCH_SIZE=64

fail() {
  echo "error: $*" >&2
  exit 1
}

assert_reviewed_head() {
  [[ -z "$REVIEWED_HEAD" || "$(git rev-parse "$1")" == "$REVIEWED_HEAD" ]] ||
    fail "release head differs from reviewed commit $REVIEWED_HEAD"
}

assert_reviewed_base() {
  local head="$1"
  [[ -n "$REVIEWED_HEAD" ]] || return 0
  assert_reviewed_head "$head"
  git fetch --quiet origin main
  [[ "$(git rev-parse origin/main)" == "$(git rev-parse "$head^")" ]] ||
    fail 'main advanced since release preparation; prepare and review a fresh release instead of syncing the reviewed head'
}

prepared_release_receipt() {
  local head base
  head=$(git rev-parse HEAD)
  base=$(git rev-parse HEAD^)
  echo 'Prepared locally; no branch, PR or tag has been published.'
  jq -cn --arg version "$NEW" --arg branch "$RELEASE_BRANCH" \
    --arg head "$head" --arg base "$base" \
    '{version:$version, branch:$branch, head:$head, base:$base}'
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command not found: $1"
}

latest_release_tag() {
  git tag --sort=-creatordate | release_version::tag_filter | awk 'NR == 1 {first=$0} END {print first}'
}

origin_release_tags() {
  git ls-remote --tags --refs origin |
    awk '{sub("refs/tags/", "", $2); print $2}' |
    release_version::tag_filter
}

origin_tag_commit_oid() {
  local tag="$1" oid
  oid=$(git ls-remote --tags origin "refs/tags/$tag^{}" | awk 'NR == 1 {print $1}')
  if [[ -z "$oid" ]]; then
    oid=$(git ls-remote --tags --refs origin "refs/tags/$tag" | awk 'NR == 1 {print $1}')
  fi
  printf '%s\n' "$oid"
}

fetch_origin_release_tag_commit() {
  local tag="$1" remote_oid evidence_ref fetched_oid
  remote_oid=$(origin_tag_commit_oid "$tag")
  [[ "$remote_oid" =~ ^[0-9a-f]{40}$ ]] ||
    fail "origin release tag has no exact object: $tag"
  evidence_ref="refs/paimos/release-origin-tags/$tag"
  git fetch --quiet origin "+refs/tags/$tag:$evidence_ref" ||
    fail "could not fetch exact origin release tag: $tag"
  fetched_oid=$(git rev-parse "$evidence_ref^{commit}" 2>/dev/null) ||
    fail "origin release tag does not resolve to a commit: $tag"
  [[ "$fetched_oid" == "$remote_oid" ]] ||
    fail "fetched release tag differs from exact origin ref: $tag"
  printf '%s\n' "$fetched_oid"
}

snapshot_origin_release_tag_refs() {
  local output="$1" raw="$2" normalized="$3"
  local oid ref extra tag filtered

  if ! git ls-remote --tags origin > "$raw"; then
    fail "could not snapshot origin release tags"
  fi
  : > "$normalized"
  while IFS=$'\t' read -r oid ref extra; do
    [[ -n "$oid" && -n "$ref" && -z "$extra" &&
       "$oid" =~ ^[0-9a-f]{40}$ && "$ref" == refs/tags/* ]] ||
      fail "origin tag snapshot contains a malformed ref"
    tag="${ref#refs/tags/}"
    tag="${tag%\^\{\}}"
    filtered=$(printf '%s\n' "$tag" | release_version::tag_filter)
    [[ "$filtered" == "$tag" ]] || continue
    case "$ref" in
      *'^{}') printf '%s\tpeeled\t%s\n' "$tag" "$oid" >> "$normalized" ;;
      *) printf '%s\tdirect\t%s\n' "$tag" "$oid" >> "$normalized" ;;
    esac
  done < "$raw"

  awk -F '\t' '
    $2 == "direct" {
      if ($1 in direct) bad = 1
      direct[$1] = $3
      tags[$1] = 1
      next
    }
    $2 == "peeled" {
      if ($1 in peeled) bad = 1
      peeled[$1] = $3
      tags[$1] = 1
      next
    }
    { bad = 1 }
    END {
      for (tag in tags) {
        if (!(tag in direct)) bad = 1
        else print tag "\t" direct[tag] "\t" ((tag in peeled) ? peeled[tag] : "-")
      }
      if (bad) exit 1
    }
  ' "$normalized" | LC_ALL=C sort > "$output" ||
    fail "origin release-tag snapshot contains contradictory or missing refs"
}

assert_no_release_tag_after_merge() {
  local release_merge="$1" audit_dir before after raw normalized
  local tag remote_object peeled evidence_ref fetched_object fetched_type tag_oid
  local batch_count=0
  local -a refspecs=()

  audit_dir=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-tag-audit.XXXXXX")
  before="$audit_dir/before"
  after="$audit_dir/after"
  raw="$audit_dir/raw"
  normalized="$audit_dir/normalized"
  snapshot_origin_release_tag_refs "$before" "$raw" "$normalized"
  [[ -s "$before" ]] || fail "origin release-tag snapshot is empty"

  while IFS=$'\t' read -r tag remote_object peeled; do
    refspecs+=("+refs/tags/$tag:refs/paimos/release-origin-tags/$tag")
    batch_count=$((batch_count + 1))
    if [[ "$batch_count" -eq "$RELEASE_TAG_FETCH_BATCH_SIZE" ]]; then
      git fetch --quiet --no-tags origin "${refspecs[@]}" ||
        fail "could not fetch exact origin release-tag evidence batch"
      refspecs=()
      batch_count=0
    fi
  done < "$before"
  if [[ "$batch_count" -gt 0 ]]; then
    git fetch --quiet --no-tags origin "${refspecs[@]}" ||
      fail "could not fetch exact origin release-tag evidence batch"
  fi

  snapshot_origin_release_tag_refs "$after" "$raw" "$normalized"
  cmp -s "$before" "$after" ||
    fail "origin release tags moved, disappeared, or appeared during evidence fetch"

  while IFS=$'\t' read -r tag remote_object peeled; do
    evidence_ref="refs/paimos/release-origin-tags/$tag"
    fetched_object=$(git rev-parse "$evidence_ref" 2>/dev/null) ||
      fail "fetched release-tag evidence ref is missing: $tag"
    [[ "$fetched_object" == "$remote_object" ]] ||
      fail "fetched release tag differs from exact origin ref: $tag"
    fetched_type=$(git cat-file -t "$fetched_object" 2>/dev/null) ||
      fail "fetched release tag has no readable object: $tag"
    if [[ "$peeled" == "-" ]]; then
      [[ "$fetched_type" == "commit" ]] ||
        fail "origin release tag is missing commit peel evidence: $tag"
      tag_oid="$fetched_object"
    else
      [[ "$fetched_type" == "tag" ]] ||
        fail "origin release tag has contradictory peel evidence: $tag"
      tag_oid=$(git rev-parse "$evidence_ref^{commit}" 2>/dev/null) ||
        fail "origin release tag does not resolve to a commit: $tag"
      [[ "$tag_oid" == "$peeled" ]] ||
        fail "fetched release tag peel differs from exact origin ref: $tag"
    fi
    [[ "$tag" == "$NEW_TAG" ]] && continue
    git merge-base --is-ancestor "$tag_oid" "$release_merge" ||
      fail "origin release tag is newer than or divergent from the interrupted release merge: $tag"
  done < "$after"
  rm -rf "$audit_dir"
}

assert_origin_calendar_recut_evidence() {
  local version="$1" existing_tags="$2" day tag stripped fetched_oid
  local evidence_count=0
  release_version::has_recut_suffix "$version" || return 0
  day=$(release_version::calendar_day "$version")
  while IFS= read -r tag; do
    stripped="${tag#v}"
    if [[ "$stripped" == "$version" || ( "$stripped" != "$day" && "$stripped" != "$day".* ) ]]; then
      continue
    fi
    fetched_oid=$(fetch_origin_release_tag_commit "$tag")
    git merge-base --is-ancestor "$fetched_oid" origin/main ||
      fail "origin prior calendar tag is not an ancestor of origin/main: $tag"
    [[ "$(git show "$fetched_oid:VERSION" 2>/dev/null || true)" == "$stripped" ]] ||
      fail "origin prior calendar tag $tag does not carry VERSION=$stripped"
    evidence_count=$((evidence_count + 1))
  done <<<"$existing_tags"
  (( evidence_count > 0 )) ||
    fail "calendar recut has no authoritative prior same-day release on origin"
}

assert_exact_calendar_recovery_main() {
  local candidate="$1" origin_main remote_main
  git fetch --quiet origin main
  origin_main=$(git rev-parse origin/main)
  remote_main=$(git ls-remote --heads origin refs/heads/main | awk 'NR == 1 {print $1}')
  [[ "$remote_main" =~ ^[0-9a-f]{40}$ && "$origin_main" == "$remote_main" && "$candidate" == "$origin_main" ]] ||
    fail "calendar recovery candidate is not the exact current protected origin/main head"
}

assert_calendar_descendant_recovery() {
  local release_merge="$1" candidate="$2" tag_state="$3"
  local remote_tag changed file required
  local required_files=$'.github/workflows/backend-full.yml\nscripts/test-backend-pr-gate.sh\nscripts/wait-backend-full.sh'

  assert_calendar_cut_day "calendar descendant recovery validation"
  assert_exact_calendar_recovery_main "$candidate"
  [[ "$candidate" != "$release_merge" ]] ||
    fail "calendar descendant recovery requires a commit after the interrupted release merge"
  git merge-base --is-ancestor "$release_merge" "$candidate" ||
    fail "calendar recovery candidate does not descend from the interrupted release merge"
  assert_release_files_at "$candidate" "$NEW"

  remote_tag=$(origin_tag_commit_oid "$NEW_TAG")
  case "$tag_state" in
    absent)
      [[ -z "$remote_tag" ]] || fail "origin/$NEW_TAG appeared during calendar descendant recovery"
      ;;
    exact)
      [[ "$remote_tag" == "$candidate" ]] ||
        fail "origin/$NEW_TAG does not point to the exact calendar recovery head $candidate"
      ;;
    *) fail "unknown calendar descendant recovery tag state: $tag_state" ;;
  esac

  changed=$(changed_commit_files "$release_merge" "$candidate")
  [[ -n "$changed" ]] || fail "calendar descendant recovery has no corrective delta"
  while IFS= read -r file; do
    case "$file" in
      .github/workflows/backend-full.yml|scripts/backend-pr-race.sh|scripts/release.sh|scripts/test-backend-pr-gate.sh|scripts/test-release.sh|scripts/wait-backend-full.sh) ;;
      *) fail "calendar descendant recovery contains an unrelated file: $file" ;;
    esac
  done <<<"$changed"
  while IFS= read -r required; do
    grep -qxF "$required" <<<"$changed" ||
      fail "calendar descendant recovery is missing required timeout correction: $required"
  done <<<"$required_files"
  assert_no_release_tag_after_merge "$release_merge"
  # The tag-history audit above performs remote list/fetch operations. Pin main
  # again after that potentially long audit so tag creation cannot use a
  # candidate that stopped being the protected-main head during the audit.
  assert_exact_calendar_recovery_main "$candidate"
}

assert_audited_release_recovery_main() {
  local release_merge="$1" candidate remote_main changed file receipt_path receipt_oid
  local receipt_seen=0

  [[ "$AUDITED_RELEASE_RECOVERY_MERGE_OID" == "$release_merge" ]] ||
    fail "audited release recovery merge changed after receipt validation"
  [[ -n "$AUDITED_RELEASE_RECOVERY_RECEIPT_OID" ]] ||
    fail "audited release recovery has no validated receipt object"
  assert_calendar_cut_day "audited release recovery validation"

  git fetch --quiet origin main
  candidate=$(git rev-parse origin/main)
  remote_main=$(git ls-remote --heads origin refs/heads/main | awk 'NR == 1 {print $1}')
  [[ "$remote_main" =~ ^[0-9a-f]{40}$ && "$candidate" == "$remote_main" ]] ||
    fail "audited release recovery is not based on exact current protected origin/main"
  [[ "$candidate" != "$release_merge" ]] ||
    fail "audited release recovery requires a reviewed commit after the release merge"
  git merge-base --is-ancestor "$release_merge" "$candidate" ||
    fail "audited release recovery main does not descend from the protected release merge"

  receipt_path="$RECOVERY_RECEIPT_DIR/$NEW_TAG.json"
  changed=$(changed_commit_files "$release_merge" "$candidate")
  [[ -n "$changed" ]] || fail "audited release recovery has no reviewed delta"
  while IFS= read -r file; do
    case "$NEW_TAG:$file" in
      v26.09.01:scripts/release.sh|\
      v26.09.01:scripts/release/recovery/v26.09.01.json|\
      v26.09.01:scripts/test-release.sh|\
      v26.09.02:scripts/release.sh|\
      v26.09.02:scripts/release/recovery/v26.09.02.json|\
      v26.09.02:scripts/test-release.sh|\
      v26.09.09:scripts/release.sh|\
      v26.09.09:scripts/release/recovery/v26.09.09.json|\
      v26.09.09:scripts/test-release.sh|\
      v260910221338.0.0:scripts/release.sh|\
      v260910221338.0.0:scripts/release/recovery/v260910221338.0.0.json|\
      v260910221338.0.0:scripts/test-release.sh|\
      v260922104613.0.0:scripts/release.sh|\
      v260922104613.0.0:scripts/release/recovery/v260922104613.0.0.json|\
      v260922104613.0.0:scripts/test-release.sh)
        ;;
      *) fail "audited release recovery contains an unrelated file: $file" ;;
    esac
    [[ "$file" != "$receipt_path" ]] || receipt_seen=1
  done <<<"$changed"
  [[ "$receipt_seen" -eq 1 ]] ||
    fail "audited release recovery delta is missing its exact receipt: $receipt_path"

  receipt_oid=$(git rev-parse "origin/main:$receipt_path" 2>/dev/null) ||
    fail "audited release recovery receipt disappeared from current origin/main"
  [[ "$receipt_oid" == "$AUDITED_RELEASE_RECOVERY_RECEIPT_OID" ]] ||
    fail "audited release recovery receipt changed after validation"
  assert_no_release_tag_after_merge "$release_merge"

  # The tag-history audit performs remote work. Pin both protected main and
  # the validated receipt object again before the caller can materialize a tag.
  git fetch --quiet origin main
  candidate=$(git rev-parse origin/main)
  remote_main=$(git ls-remote --heads origin refs/heads/main | awk 'NR == 1 {print $1}')
  [[ "$remote_main" =~ ^[0-9a-f]{40}$ && "$candidate" == "$remote_main" ]] ||
    fail "origin/main moved during audited release recovery validation"
  receipt_oid=$(git rev-parse "origin/main:$receipt_path" 2>/dev/null) ||
    fail "audited release recovery receipt disappeared during validation"
  [[ "$receipt_oid" == "$AUDITED_RELEASE_RECOVERY_RECEIPT_OID" ]] ||
    fail "audited release recovery receipt changed during validation"
}

assert_calendar_cut_day() {
  local context="$1"
  if release_version::is_calendar "$NEW" && ! release_version::is_calendar_cut_today "$NEW"; then
    fail "Vienna calendar day changed before $context; refusing release $NEW"
  fi
  if release_version::is_calendar_v2 "$NEW" &&
     [[ "$(release_version::calendar_v2_day "$NEW")" != "$(release_version::utc_date_compact)" ]]; then
    fail "UTC calendar day changed before $context; refusing release $NEW"
  fi
}

changed_worktree_files() {
  { git diff --name-only HEAD; git diff --cached --name-only; git ls-files --others --exclude-standard; } |
    LC_ALL=C sort -u | grep -v '^$' || true
}

changed_commit_files() {
  local base="$1" head="$2"
  git diff --name-only "$base" "$head" | LC_ALL=C sort -u | grep -v '^$' || true
}

assert_expected_file_set() {
  local actual="$1" context="$2"
  if [[ "$actual" != "$EXPECTED_FILES" ]]; then
    echo "error: $context changed files do not match the release contract" >&2
    echo "expected:" >&2
    printf '%s\n' "$EXPECTED_FILES" | sed 's/^/  /' >&2
    echo "actual:" >&2
    printf '%s\n' "$actual" | sed 's/^/  /' >&2
    exit 1
  fi
}

assert_release_files_at() {
  local commit="$1" version="$2"
  local content

  git cat-file -e "${commit}^{commit}" 2>/dev/null || fail "release commit is unavailable: $commit"
  [[ "$(git show "$commit:VERSION")" == "$version" ]] ||
    fail "$commit does not carry VERSION=$version"

  content=$(git show "$commit:README.md")
  grep -qF "<code>v$version</code>" <<<"$content" ||
    fail "$commit does not carry the v$version README badge"

  content=$(git show "$commit:docs/CHANGELOG.md")
  grep -qF "## [$version] — " <<<"$content" ||
    fail "$commit does not carry the $version changelog entry"
  if grep -qF 'TODO fill in before committing' <<<"$content"; then
    fail "$commit still contains the unreviewed changelog placeholder"
  fi

  content=$(git show "$commit:docs/INSTALL.md")
  [[ $(grep -cF "VER=$version" <<<"$content") -ge 2 ]] ||
    fail "$commit does not carry both pinned install examples for $version"
  grep -qE "paimos --version[[:space:]]+# $version" <<<"$content" ||
    fail "$commit does not carry the $version CLI smoke example"
}

# The external-stage v1 manifest is deliberately outside the four mutable
# release files: adapters pin the first reviewed Paimos release forever. The
# manifest metadata is not self-compared because its commit pin necessarily
# predates that metadata; the contract and fixture bytes are compared instead.
# For the first release the future tag is allowed. After that, the tag must
# resolve, retain the pinned commit in its history, and carry the exact same
# immutable manifest as the release ref.
assert_external_stage_release_pin() {
  local ref="$1" new_tag="$2" manifest pin_record release_pin commit_pin
  local pinned_file current_oid pinned_oid
  local -a pinned_files=(
    'backend/contracts/fixtures/external-stage/dependency-janus-v1.json'
    'backend/contracts/fixtures/external-stage/owner-pharos-v1.json'
    'backend/externalstage/contract.go'
  )

  git cat-file -e "$ref:$EXTERNAL_STAGE_MANIFEST" 2>/dev/null ||
    fail "$ref does not carry the external-stage v1 release manifest"
  manifest=$(git show "$ref:$EXTERNAL_STAGE_MANIFEST")
  pin_record=$(jq -er '
    if type == "object"
      and (.paimos_release | type) == "string"
      and (.paimos_commit | type) == "string"
    then [.paimos_release, .paimos_commit] | @tsv
    else error("missing paimos_release or paimos_commit")
    end
  ' <<<"$manifest") || fail "$ref carries an invalid external-stage v1 release manifest"
  IFS=$'\t' read -r release_pin commit_pin <<<"$pin_record"

  [[ "$release_pin" != 'PENDING_RELEASE_TAG' ]] ||
    fail "external-stage v1 still has PENDING_RELEASE_TAG; finalize its immutable Paimos release pin before publishing"
  [[ "$release_pin" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "external-stage v1 paimos_release is not a stable SemVer tag: $release_pin"
  [[ "$commit_pin" =~ ^[0-9a-f]{40}$ ]] ||
    fail "external-stage v1 paimos_commit is not a lowercase 40-hex commit: $commit_pin"
  git cat-file -e "$commit_pin^{commit}" 2>/dev/null ||
    fail "external-stage v1 paimos_commit is unavailable: $commit_pin"
  git merge-base --is-ancestor "$commit_pin" "$ref" ||
    fail "external-stage v1 paimos_commit is not an ancestor of $ref: $commit_pin"

  for pinned_file in "${pinned_files[@]}"; do
    git cat-file -e "$commit_pin:$pinned_file" 2>/dev/null ||
      fail "external-stage v1 pinned commit does not carry $pinned_file"
    git cat-file -e "$ref:$pinned_file" 2>/dev/null ||
      fail "$ref does not carry pinned external-stage v1 file $pinned_file"
    current_oid=$(git rev-parse "$ref:$pinned_file")
    pinned_oid=$(git rev-parse "$commit_pin:$pinned_file")
    [[ "$current_oid" == "$pinned_oid" ]] ||
      fail "external-stage v1 file differs from immutable pinned commit $commit_pin: $pinned_file"
  done

  if git cat-file -e "refs/tags/$release_pin^{commit}" 2>/dev/null; then
    git merge-base --is-ancestor "$commit_pin" "refs/tags/$release_pin^{commit}" ||
      fail "external-stage v1 pinned tag $release_pin does not contain commit $commit_pin"
    git cat-file -e "refs/tags/$release_pin:$EXTERNAL_STAGE_MANIFEST" 2>/dev/null ||
      fail "external-stage v1 pinned tag $release_pin does not carry its manifest"
    current_oid=$(git rev-parse "$ref:$EXTERNAL_STAGE_MANIFEST")
    pinned_oid=$(git rev-parse "refs/tags/$release_pin:$EXTERNAL_STAGE_MANIFEST")
    [[ "$current_oid" == "$pinned_oid" ]] ||
      fail "external-stage v1 manifest differs from immutable pinned tag $release_pin"
  else
    [[ "$release_pin" == "$new_tag" ]] ||
      fail "external-stage v1 pins unavailable tag $release_pin instead of the release being prepared ($new_tag)"
  fi
}

assert_external_stage_v2_release_pin() {
  local ref="$1" new_tag="$2" manifest pin_record release_pin commit_pin pinned_file current_oid pinned_oid
  local -a pinned_files=(
    'backend/contracts/fixtures/external-stage-v2/owner-pharos-v2.json'
    'backend/contracts/external-stage-v2.schema.json'
    'backend/externalstage/contract_v2.go'
    'backend/externalstage/service_v2.go'
  )

  if ! git cat-file -e "$ref:$EXTERNAL_STAGE_MANIFEST_V2" 2>/dev/null; then
    for pinned_file in "${pinned_files[@]}"; do
      ! git cat-file -e "$ref:$pinned_file" 2>/dev/null ||
        fail "$ref carries external-stage v2 bytes without the required release manifest"
    done
    return
  fi
  manifest=$(git show "$ref:$EXTERNAL_STAGE_MANIFEST_V2")
  pin_record=$(jq -er '
    if type == "object" and .schema_major == 2
      and .media_type == "application/vnd.paimos.external-stage.v2+json"
      and (.paimos_release | type) == "string" and (.paimos_commit | type) == "string"
    then [.paimos_release, .paimos_commit] | @tsv
    else error("invalid v2 certification tuple") end
  ' <<<"$manifest") || fail "$ref carries an invalid external-stage v2 release manifest"
  IFS=$'\t' read -r release_pin commit_pin <<<"$pin_record"
  release_version::is_any_calendar "$release_pin" ||
    fail "external-stage v2 paimos_release is not an INSPR calendar tag: $release_pin"
  [[ "$commit_pin" =~ ^[0-9a-f]{40}$ ]] || fail "external-stage v2 paimos_commit is not lowercase 40-hex"
  git cat-file -e "$commit_pin^{commit}" 2>/dev/null || fail "external-stage v2 pinned commit is unavailable: $commit_pin"
  git merge-base --is-ancestor "$commit_pin" "$ref" || fail "external-stage v2 pinned commit is not an ancestor of $ref"
  for pinned_file in "${pinned_files[@]}"; do
    current_oid=$(git rev-parse "$ref:$pinned_file")
    pinned_oid=$(git rev-parse "$commit_pin:$pinned_file")
    [[ "$current_oid" == "$pinned_oid" ]] || fail "external-stage v2 file differs from pinned commit: $pinned_file"
  done
  if git cat-file -e "refs/tags/$release_pin^{commit}" 2>/dev/null; then
    git merge-base --is-ancestor "$commit_pin" "refs/tags/$release_pin^{commit}" ||
      fail "external-stage v2 pinned tag does not contain its certified commit"
    current_oid=$(git rev-parse "$ref:$EXTERNAL_STAGE_MANIFEST_V2")
    pinned_oid=$(git rev-parse "refs/tags/$release_pin:$EXTERNAL_STAGE_MANIFEST_V2")
    [[ "$current_oid" == "$pinned_oid" ]] || fail "external-stage v2 manifest differs from immutable pinned tag"
  else
    [[ "$release_pin" == "$new_tag" ]] ||
      fail "external-stage v2 pins unavailable tag $release_pin instead of release $new_tag"
  fi
}

assert_release_delta() {
  local base="$1" head="$2" version="$3"
  local first_heading entry_count changed entry_date validate_tmp history_bytes base_first_heading base_second_heading head_second_heading

  changed=$(changed_commit_files "$base" "$head")
  assert_expected_file_set "$changed" "$head relative to $base"
  assert_release_files_at "$head" "$version"

  validate_tmp=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-validate.XXXXXX")
  git show "$head:VERSION" > "$validate_tmp/actual"
  printf '%s\n' "$version" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic VERSION content"
  fi

  git show "$head:README.md" > "$validate_tmp/actual"
  git show "$base:README.md" |
    sed -E "s~<code>v${PAIMOS_RELEASE_VERSION_ERE}</code>~<code>v$version</code>~" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic README.md changes"
  fi

  git show "$head:docs/INSTALL.md" > "$validate_tmp/actual"
  git show "$base:docs/INSTALL.md" |
    sed -E \
      -e "s~VER=${PAIMOS_RELEASE_VERSION_ERE}~VER=$version~g" \
      -e "s~(paimos --version[[:space:]]+# )${PAIMOS_RELEASE_VERSION_ERE}~\1$version~" > "$validate_tmp/expected"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head contains non-deterministic docs/INSTALL.md changes"
  fi

  git show "$base:docs/CHANGELOG.md" > "$validate_tmp/base-changelog"
  git show "$head:docs/CHANGELOG.md" > "$validate_tmp/head-changelog"
  sed '/^## \[/,$d' "$validate_tmp/base-changelog" > "$validate_tmp/expected"
  sed '/^## \[/,$d' "$validate_tmp/head-changelog" > "$validate_tmp/actual"
  if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
    rm -rf "$validate_tmp"
    fail "$head changed the CHANGELOG preamble"
  fi

  first_heading=$(awk '/^## \[/{print; exit}' "$validate_tmp/head-changelog")
  entry_date=${first_heading#"## [$version] — "}
  if [[ "$first_heading" == "$entry_date" || ! "$entry_date" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    rm -rf "$validate_tmp"
    fail "$head does not prepend [$version] with an ISO date"
  fi
  entry_count=$(awk -v heading="## [$version] " \
    'index($0, heading) == 1 {count++} END {print count+0}' "$validate_tmp/head-changelog")
  if [[ "$entry_count" != "1" ]]; then
    rm -rf "$validate_tmp"
    fail "$head must contain exactly one [$version] CHANGELOG entry"
  fi

  base_first_heading=$(awk '/^## \[/{print; exit}' "$validate_tmp/base-changelog")
  if [[ "$base_first_heading" == "## [Unreleased]" ]]; then
    base_second_heading=$(awk '/^## \[/{headings++; if (headings == 2) {print; exit}}' "$validate_tmp/base-changelog")
    if [[ ! "$base_second_heading" =~ ^##\ \[${PAIMOS_RELEASE_VERSION_ERE}\]\ —\ [0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
      rm -rf "$validate_tmp"
      fail "$base carries a duplicate or non-canonical leading [Unreleased] CHANGELOG section"
    fi
    awk -v replacement="## [$version] — $entry_date" \
      '!consumed && $0 == "## [Unreleased]" {$0=replacement; consumed=1} {print}' \
      "$validate_tmp/base-changelog" > "$validate_tmp/expected"
    if ! cmp -s "$validate_tmp/head-changelog" "$validate_tmp/expected"; then
      rm -rf "$validate_tmp"
      fail "$head did not consume [Unreleased] exactly or changed prior CHANGELOG history"
    fi
  else
    if [[ "$base_first_heading" == "## [Unreleased]"* ]]; then
      rm -rf "$validate_tmp"
      fail "$base carries a non-canonical leading [Unreleased] CHANGELOG section"
    fi
    sed -n '/^## \[/,$p' "$validate_tmp/base-changelog" > "$validate_tmp/expected"
    history_bytes=$(wc -c < "$validate_tmp/expected" | tr -d '[:space:]')
    tail -c "$history_bytes" "$validate_tmp/head-changelog" > "$validate_tmp/actual"
    if ! cmp -s "$validate_tmp/actual" "$validate_tmp/expected"; then
      rm -rf "$validate_tmp"
      fail "$head changed prior CHANGELOG history"
    fi

    head_second_heading=$(awk '/^## \[/{headings++; if (headings == 2) {print; exit}}' "$validate_tmp/head-changelog")
    if [[ "$head_second_heading" != "$base_first_heading" ]]; then
      rm -rf "$validate_tmp"
      fail "$head inserted unexpected CHANGELOG sections before prior history"
    fi
  fi
  rm -rf "$validate_tmp"
}

run_release_gates_at_ref() {
  local ref="$1" gate_tmp gate_status=0
  gate_tmp=$(mktemp -d "${TMPDIR:-/tmp}/paimos-release-gates.XXXXXX")
  rmdir "$gate_tmp"
  git worktree add --quiet --detach "$gate_tmp" "$ref"
  "$gate_tmp/scripts/check-claims.sh" || gate_status=1
  "$gate_tmp/scripts/check-release-hygiene.sh" || gate_status=1
  git worktree remove --force "$gate_tmp"
  [[ "$gate_status" -eq 0 ]] || fail "release gates failed at validated PR head $ref"
}

remote_branch_exists() {
  git ls-remote --exit-code --heads origin "$RELEASE_BRANCH" >/dev/null 2>&1
}

fetch_release_branch() {
  git fetch --quiet origin "+refs/heads/$RELEASE_BRANCH:refs/remotes/origin/$RELEASE_BRANCH"
}

assert_release_branch() {
  local head="origin/$RELEASE_BRANCH"
  local base changed

  git cat-file -e "${head}^{commit}" 2>/dev/null ||
    fail "remote release branch is unavailable: $RELEASE_BRANCH"
  if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
    [[ "$(git rev-parse "$RELEASE_BRANCH")" == "$(git rev-parse "$head")" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
  fi
  base=$(git merge-base origin/main "$head")
  [[ -n "$base" ]] || fail "release branch has no merge-base with origin/main"
  changed=$(changed_commit_files "$base" "$head")
  assert_expected_file_set "$changed" "origin/$RELEASE_BRANCH"
  assert_release_delta "$base" "$head" "$NEW"
  "$ROOT/scripts/check-dco.sh" "$base" "$head" >/dev/null ||
    fail "release branch contains a commit without its author's DCO sign-off"
}

fetch_and_validate_pr_head() {
  local json="$1" api_head fetched_head base
  api_head=$(printf '%s\n' "$json" | jq -r '.headRefOid // empty')
  [[ -n "$api_head" ]] || fail "release PR has no head OID"
  PR_HEAD_REF="refs/paimos/release-prs/$PR_NUMBER/head"
  git fetch --quiet origin "+refs/pull/$PR_NUMBER/head:$PR_HEAD_REF"
  fetched_head=$(git rev-parse "$PR_HEAD_REF")
  [[ "$fetched_head" == "$api_head" ]] ||
    fail "fetched PR head $fetched_head differs from API head $api_head"
  assert_reviewed_head "$PR_HEAD_REF"
  base=$(git merge-base origin/main "$PR_HEAD_REF")
  [[ -n "$base" ]] || fail "release PR head has no merge-base with origin/main"
  assert_release_delta "$base" "$PR_HEAD_REF" "$NEW"
  "$ROOT/scripts/check-dco.sh" "$base" "$PR_HEAD_REF" >/dev/null ||
    fail "release PR head contains a commit without its author's DCO sign-off"
  VALIDATED_HEAD_OID="$fetched_head"
}

assert_pr_head_pinned() {
  local json="$1" api_head fetched_head
  api_head=$(printf '%s\n' "$json" | jq -r '.headRefOid // empty')
  [[ "$api_head" == "$VALIDATED_HEAD_OID" ]] ||
    fail "release PR head changed after validation: expected $VALIDATED_HEAD_OID, got $api_head"
  git fetch --quiet origin "+refs/pull/$PR_NUMBER/head:$PR_HEAD_REF"
  fetched_head=$(git rev-parse "$PR_HEAD_REF")
  [[ "$fetched_head" == "$VALIDATED_HEAD_OID" ]] ||
    fail "fetched release PR head changed after validation"
}

assert_squash_auto_merge() {
  local json="$1" method
  method=$(printf '%s\n' "$json" | jq -r '.autoMergeRequest.mergeMethod // empty')
  [[ "$method" == "SQUASH" ]] ||
    fail "release PR is missing protected squash auto-merge provenance (got: ${method:-none})"
}

assert_release_recovery_receipt() {
  local pr_json="$1" receipt_path receipt mode receipt_fields
  local receipt_release receipt_pr receipt_head receipt_merge receipt_reason
  local live_pr live_head live_merge parent_count merge_tree head_tree checks

  receipt_path="$RECOVERY_RECEIPT_DIR/$NEW_TAG.json"
  git fetch --quiet origin main
  mode=$(git ls-tree origin/main -- "$receipt_path" | awk 'NR == 1 {print $1}')
  [[ "$mode" == "100644" ]] ||
    fail "missing tracked release recovery receipt on origin/main: $receipt_path"
  receipt=$(git show "origin/main:$receipt_path") ||
    fail "could not read release recovery receipt from origin/main: $receipt_path"
  receipt_fields=$(jq -er '
    if type == "object"
      and (keys | sort) == ([
        "approved_head",
        "incident_reason",
        "merge_commit",
        "pull_request",
        "release",
        "schema_version"
      ] | sort)
      and .schema_version == 1
      and (.release | type) == "string"
      and (.pull_request | type) == "number"
      and (.approved_head | type) == "string"
      and (.merge_commit | type) == "string"
      and (.incident_reason | type) == "string"
    then [
      .release,
      (.pull_request | tostring),
      .approved_head,
      .merge_commit,
      .incident_reason
    ] | @tsv
    else error("invalid recovery receipt")
    end
  ' <<<"$receipt") || fail "invalid release recovery receipt: $receipt_path"
  IFS=$'\t' read -r receipt_release receipt_pr receipt_head receipt_merge receipt_reason <<<"$receipt_fields"

  [[ "$receipt_release" == "$NEW_TAG" ]] ||
    fail "release recovery receipt targets $receipt_release instead of $NEW_TAG"
  [[ "$receipt_pr" == "$PR_NUMBER" ]] ||
    fail "release recovery receipt targets PR $receipt_pr instead of PR $PR_NUMBER"
  [[ "$receipt_head" =~ ^[0-9a-f]{40}$ ]] ||
    fail "release recovery receipt approved_head is not lowercase 40-hex"
  [[ "$receipt_merge" =~ ^[0-9a-f]{40}$ ]] ||
    fail "release recovery receipt merge_commit is not lowercase 40-hex"
  case "$receipt_release:$receipt_reason" in
    v5.19.0:manual_squash_merge_missing_auto_merge_provenance)
      ;;
    v5.20.0:canonical_auto_merge_immediate_merge_post_merge_request_missing)
      # GitHub can merge immediately while `gh pr merge --auto` is enabling
      # canonical squash auto-merge, then omit autoMergeRequest from the
      # post-merge PR response. The reviewed receipt remains mandatory and
      # every pinned head/check/tree/ancestry/tag gate below still applies.
      ;;
    v26.09.01:canonical_auto_merge_immediate_merge_post_merge_request_missing)
      # PAI-874 records the same GitHub immediate-merge incident for PR #193.
      # Keep this release-specific: the audited receipt and every fail-closed
      # head/check/tree/ancestry/tag gate below remain mandatory.
      ;;
    v26.09.02:manual_squash_merge_missing_auto_merge_provenance)
      # PAI-886 records the direct protected squash of PR #201. Keep this
      # release-specific: the audited receipt and every fail-closed
      # head/check/tree/ancestry/tag gate below remain mandatory.
      ;;
    v26.09.09:protected_squash_merge_missing_auto_merge_provenance)
      # PAI-977 records only the observed protected squash of PR #253 and the
      # missing post-merge autoMergeRequest value. It deliberately does not
      # infer whether --auto was used. Keep every pinned gate below mandatory.
      ;;
    v260910221338.0.0:canonical_auto_merge_immediate_merge_post_merge_request_missing)
      # PAI-994 records the same GitHub immediate-merge incident for PR #281.
      # Keep this release-specific: the audited receipt and every fail-closed
      # head/check/tree/ancestry/tag gate below remain mandatory.
      ;;
    v260922104613.0.0:canonical_auto_merge_immediate_merge_post_merge_request_missing)
      # PAI-1054: PR #358 merged immediately through the canonical protected
      # --auto --squash --match-head-commit command after every check was green.
      # GitHub omitted the post-merge request; retain every exact receipt gate.
      ;;
    *)
      fail "release recovery receipt carries an unrecognized incident reason"
      ;;
  esac

  live_pr=$(printf '%s\n' "$pr_json" | jq -r '.number // empty')
  live_head=$(printf '%s\n' "$pr_json" | jq -r '.headRefOid // empty')
  live_merge=$(printf '%s\n' "$pr_json" | jq -r '.mergeCommit.oid // empty')
  [[ "$receipt_pr" == "$live_pr" ]] ||
    fail "release recovery receipt PR does not match live GitHub PR JSON"
  [[ "$receipt_head" == "$live_head" && "$receipt_head" == "$VALIDATED_HEAD_OID" ]] ||
    fail "release recovery receipt approved head does not match live validated PR head"
  [[ "$receipt_merge" == "$live_merge" ]] ||
    fail "release recovery receipt merge does not match live GitHub PR JSON"

  git cat-file -e "$receipt_merge^{commit}" 2>/dev/null ||
    fail "release recovery merge commit is unavailable: $receipt_merge"
  git merge-base --is-ancestor "$receipt_merge" origin/main ||
    fail "release recovery merge is not an ancestor of current origin/main"
  parent_count=$(git rev-list --parents -n1 "$receipt_merge" | awk '{print NF-1}')
  [[ "$parent_count" == "1" ]] ||
    fail "release recovery merge is not a one-parent squash (got $parent_count parents)"
  merge_tree=$(git rev-parse "$receipt_merge^{tree}")
  head_tree=$(git rev-parse "$PR_HEAD_REF^{tree}")
  [[ "$merge_tree" == "$head_tree" ]] ||
    fail "release recovery merge tree differs from approved PR head $receipt_head"

  if git show-ref --verify --quiet "refs/tags/$NEW_TAG" ||
     git ls-remote --exit-code --tags origin "refs/tags/$NEW_TAG" >/dev/null 2>&1; then
    fail "release recovery requires absent tag $NEW_TAG"
  fi

  checks=$(gh pr checks "$PR_NUMBER" \
    --repo "$REPO" \
    --required \
    --json name,state,bucket,workflow) ||
    fail "could not verify required checks for approved PR head $receipt_head"
  jq -e '
    type == "array"
      and length > 0
      and all(.[];
        (.name | type) == "string"
          and (.name | length) > 0
          and (.state == "SUCCESS" or .state == "SKIPPED")
      )
  ' >/dev/null <<<"$checks" ||
    fail "approved PR head has missing, pending, or failed required checks"

  AUDITED_RELEASE_RECOVERY_MERGE_OID="$receipt_merge"
  AUDITED_RELEASE_RECOVERY_RECEIPT_OID=$(git rev-parse "origin/main:$receipt_path")
  echo "Accepted audited recovery receipt for $NEW_TAG / PR $PR_NUMBER at $receipt_merge." >&2
}

assert_merged_release_provenance() {
  local pr_json="$1" auto
  auto=$(printf '%s\n' "$pr_json" | jq -r 'if .autoMergeRequest == null then "none" else "present" end')
  if [[ "$auto" == "present" ]]; then
    assert_squash_auto_merge "$pr_json"
    return
  fi
  assert_release_recovery_receipt "$pr_json"
}

find_release_pr() {
  local json count
  json=$(gh pr list \
    --repo "$REPO" \
    --state all \
    --head "$RELEASE_BRANCH" \
    --limit 100 \
    --json number,state,url,headRefName,headRefOid,baseRefName,autoMergeRequest,mergeCommit)
  count=$(printf '%s\n' "$json" | jq 'length')
  if [[ "$count" -gt 1 ]]; then
    fail "multiple PRs exist for $RELEASE_BRANCH; refusing an ambiguous release"
  fi
  if [[ "$count" -eq 1 ]]; then
    printf '%s\n' "$json" | jq '.[0]'
  fi
}

validate_pr_identity() {
  local json="$1"
  [[ "$(printf '%s\n' "$json" | jq -r '.baseRefName')" == "main" ]] ||
    fail "release PR does not target main"
  [[ "$(printf '%s\n' "$json" | jq -r '.headRefName')" == "$RELEASE_BRANCH" ]] ||
    fail "release PR does not use $RELEASE_BRANCH"
}

assert_open_pr_head_matches_remote() {
  local json="$1" pr_head remote_head
  pr_head=$(printf '%s\n' "$json" | jq -r '.headRefOid')
  remote_head=$(git rev-parse "origin/$RELEASE_BRANCH")
  [[ "$pr_head" == "$remote_head" ]] ||
    fail "release PR head $pr_head differs from origin/$RELEASE_BRANCH at $remote_head"
}

checkout_release_branch_for_sync() {
  local remote_oid current
  fetch_release_branch
  remote_oid=$(git rev-parse "origin/$RELEASE_BRANCH")
  current=$(git rev-parse --abbrev-ref HEAD)
  [[ -z "$(changed_worktree_files)" ]] ||
    fail "working tree became dirty while waiting for release merge"

  if [[ "$current" == "$RELEASE_BRANCH" ]]; then
    [[ "$(git rev-parse HEAD)" == "$remote_oid" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
    return
  fi

  if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
    [[ "$(git rev-parse "$RELEASE_BRANCH")" == "$remote_oid" ]] ||
      fail "local $RELEASE_BRANCH differs from origin/$RELEASE_BRANCH"
    git switch "$RELEASE_BRANCH" >/dev/null
  else
    git switch -c "$RELEASE_BRANCH" --track "origin/$RELEASE_BRANCH" >/dev/null
  fi
}

sync_release_branch() {
  [[ -z "$REVIEWED_HEAD" ]] || fail 'reviewed release cannot be automatically synced; prepare and review a fresh release'
  checkout_release_branch_for_sync
  git fetch --quiet origin main
  if git merge-base --is-ancestor origin/main HEAD; then
    return
  fi

  echo "Release PR is behind main; merging current origin/main with DCO sign-off." >&2
  git merge --no-commit --no-ff --no-gpg-sign origin/main >&2
  if ! "$ROOT/scripts/check-claims.sh" >&2 ||
     ! "$ROOT/scripts/check-release-hygiene.sh" >&2; then
    git merge --abort
    fail "release gates failed after merging current origin/main"
  fi
  git commit --no-gpg-sign --signoff \
    -m "chore(release): sync $NEW_TAG with main" >&2
  git push origin "$RELEASE_BRANCH"
  fetch_release_branch
  assert_release_branch
  VALIDATED_HEAD_OID=$(git rev-parse "origin/$RELEASE_BRANCH")
}

ensure_auto_merge() {
  local pr_json="$1" auto head_oid
  auto=$(printf '%s\n' "$pr_json" | jq -r 'if .autoMergeRequest == null then "none" else "enabled" end')
  if [[ "$auto" == "enabled" ]]; then
    assert_squash_auto_merge "$pr_json"
    return
  fi
  head_oid=$(printf '%s\n' "$pr_json" | jq -r '.headRefOid')
  assert_reviewed_base "$head_oid"
  gh pr merge "$PR_NUMBER" \
    --repo "$REPO" \
    --auto \
    --squash \
    --delete-branch \
    --match-head-commit "$head_oid" >/dev/null
  echo "Protected auto-merge enabled for $PR_URL." >&2
}

required_checks_green_for_pinned_head() {
  local checks
  checks=$(gh pr checks "$PR_NUMBER" \
    --repo "$REPO" \
    --required \
    --json name,state,bucket,workflow) || return 1
  jq -e '
    type == "array"
      and length > 0
      and all(.[];
        (.name | type) == "string"
          and (.name | length) > 0
          and (.state == "SUCCESS" or .state == "SKIPPED")
      )
  ' >/dev/null <<<"$checks"
}

wait_for_protected_merge() {
  local start now pr_json state merge_status merge_oid known_green_checks=0
  start=$(date +%s)
  while true; do
    now=$(date +%s)
    if (( now - start > MERGE_TIMEOUT )); then
      fail "timed out after ${MERGE_TIMEOUT}s waiting for protected merge: $PR_URL"
    fi

    pr_json=$(gh pr view "$PR_NUMBER" \
      --repo "$REPO" \
      --json number,state,url,headRefName,headRefOid,baseRefName,mergeStateStatus,autoMergeRequest,mergeCommit)
    validate_pr_identity "$pr_json"
    assert_pr_head_pinned "$pr_json"
    state=$(printf '%s\n' "$pr_json" | jq -r '.state')
    case "$state" in
      MERGED)
        merge_oid=$(printf '%s\n' "$pr_json" | jq -r '.mergeCommit.oid // empty')
        [[ -n "$merge_oid" ]] || fail "merged release PR has no merge commit"
        assert_squash_auto_merge "$pr_json"
        MERGE_OID="$merge_oid"
        return
        ;;
      CLOSED)
        fail "release PR closed without merge: $PR_URL"
        ;;
      OPEN) ;;
      *) fail "unexpected release PR state: $state" ;;
    esac

    merge_status=$(printf '%s\n' "$pr_json" | jq -r '.mergeStateStatus // "UNKNOWN"')
    case "$merge_status" in
      BEHIND)
        sync_release_branch
        ;;
      DIRTY)
        fail "release PR has merge conflicts: $PR_URL"
        ;;
      *)
        ensure_auto_merge "$pr_json"
        ;;
    esac
    # A resumed release can reach this loop with every required check already
    # green for the still-pinned PR head. Confirm that exact evidence once,
    # distinguish the remaining protected-merge wait from a suite wait, and
    # make one immediate state query before returning to the normal poll.
    if [[ "$known_green_checks" -eq 0 ]] && required_checks_green_for_pinned_head; then
      echo "Required checks already green for exact release head $VALIDATED_HEAD_OID; waiting only for protected merge." >&2
      known_green_checks=1
      continue
    fi
    echo "Waiting for protected checks/merge ($merge_status): $PR_URL" >&2
    sleep "$MERGE_POLL"
  done
}

assert_release_merge() {
  local merge_oid="$1" changed parent_count merge_tree head_tree
  git fetch --quiet origin main
  git cat-file -e "${merge_oid}^{commit}" 2>/dev/null ||
    fail "PR merge commit was not fetched from origin/main: $merge_oid"
  git merge-base --is-ancestor "$merge_oid" origin/main ||
    fail "PR merge commit is not on origin/main: $merge_oid"
  parent_count=$(git rev-list --parents -n1 "$merge_oid" | awk '{print NF-1}')
  [[ "$parent_count" == "1" ]] ||
    fail "release PR was not squash-merged; expected one parent, got $parent_count"
  changed=$(changed_commit_files "$merge_oid^" "$merge_oid")
  assert_expected_file_set "$changed" "protected-main merge $merge_oid"
  assert_release_delta "$merge_oid^" "$merge_oid" "$NEW"
  merge_tree=$(git rev-parse "$merge_oid^{tree}")
  head_tree=$(git rev-parse "$PR_HEAD_REF^{tree}")
  [[ "$merge_tree" == "$head_tree" ]] ||
    fail "protected-main merge tree differs from validated PR head $VALIDATED_HEAD_OID"
}

select_release_tag_commit() {
  local release_merge="$1" candidate remote_tag tag_state
  TAG_OID="$release_merge"
  CALENDAR_RECOVERY_MERGE_OID=''
  release_version::is_any_calendar "$NEW" || return 0

  if [[ -n "$AUDITED_RELEASE_RECOVERY_MERGE_OID" ]]; then
    [[ "$AUDITED_RELEASE_RECOVERY_MERGE_OID" == "$release_merge" ]] ||
      fail "audited release recovery does not match the selected protected merge"
    assert_audited_release_recovery_main "$release_merge"
    echo "Audited recovery will tag exact protected release merge $TAG_OID."
    return 0
  fi

  git fetch --quiet origin main
  candidate=$(git rev-parse origin/main)
  [[ "$candidate" != "$release_merge" ]] || return 0
  remote_tag=$(origin_tag_commit_oid "$NEW_TAG")
  if [[ -z "$remote_tag" ]]; then
    tag_state=absent
  elif [[ "$remote_tag" == "$candidate" ]]; then
    tag_state=exact
  else
    fail "origin/$NEW_TAG points to $remote_tag, not the exact protected-main recovery head $candidate"
  fi
  assert_calendar_descendant_recovery "$release_merge" "$candidate" "$tag_state"
  TAG_OID="$candidate"
  CALENDAR_RECOVERY_MERGE_OID="$release_merge"
  echo "Calendar recovery will require exhaustive assurance for exact protected-main head $TAG_OID."
}

tag_release_merge() {
  local merge_oid="$1" existing_oid remote_oid recovery_tag_state
  if release_version::is_any_calendar "$NEW" && [[ -n "$AUDITED_RELEASE_RECOVERY_MERGE_OID" ]]; then
    [[ "$merge_oid" == "$AUDITED_RELEASE_RECOVERY_MERGE_OID" ]] ||
      fail "audited release recovery attempted to tag a different commit"
  fi
  if [[ -n "$CALENDAR_RECOVERY_MERGE_OID" ]]; then
    remote_oid=$(origin_tag_commit_oid "$NEW_TAG")
    recovery_tag_state=absent
    [[ -z "$remote_oid" ]] || recovery_tag_state=exact
    assert_calendar_descendant_recovery \
      "$CALENDAR_RECOVERY_MERGE_OID" "$merge_oid" "$recovery_tag_state"
  fi
  remote_oid=$(origin_tag_commit_oid "$NEW_TAG")
  if [[ -n "$remote_oid" ]]; then
    [[ "$remote_oid" == "$merge_oid" ]] ||
      fail "origin/$NEW_TAG points to $remote_oid, expected canonical protected merge $merge_oid"
    echo "origin/$NEW_TAG already tags the correct protected-main merge commit."
  else
    assert_calendar_cut_day "creating absent tag $NEW_TAG"
    if git rev-parse "$NEW_TAG" >/dev/null 2>&1; then
      existing_oid=$(git rev-list -n1 "${NEW_TAG}^{}")
      fail "$NEW_TAG exists only locally at $existing_oid; refusing unproven tag publication"
    fi
    if [[ -n "$CALENDAR_RECOVERY_MERGE_OID" ]]; then
      # The absent-tag query above is remote work after the history audit.
      # Re-pin protected main as the final operation before materializing the
      # local tag, closing movement during that query as well.
      assert_exact_calendar_recovery_main "$merge_oid"
    elif release_version::is_any_calendar "$NEW" && [[ -n "$AUDITED_RELEASE_RECOVERY_MERGE_OID" ]]; then
      # Revalidate the live receipt and exact recovery-only main delta after
      # the remote tag query. TAG_OID remains the original protected squash.
      assert_release_recovery_receipt "$PR_JSON"
      assert_audited_release_recovery_main "$merge_oid"
    fi
    # Every blocking remote audit is complete. Read the real cut-day clock as
    # the final operation before materializing an absent calendar tag.
    assert_calendar_cut_day "creating absent tag $NEW_TAG after remote audit"
    git tag -a --no-sign "$NEW_TAG" "$merge_oid" -m "release $NEW"
    echo "Created $NEW_TAG at protected-main commit $merge_oid."
    git push origin "refs/tags/$NEW_TAG"
    remote_oid=$(origin_tag_commit_oid "$NEW_TAG")
    [[ "$remote_oid" == "$merge_oid" ]] ||
      fail "origin/$NEW_TAG did not resolve to canonical protected merge $merge_oid after push"
  fi
}

prepare_release_branch() {
  local current changed tmp today base first_existing_heading second_existing_heading canonical_unreleased=0
  current=$(git rev-parse --abbrev-ref HEAD)
  changed=$(changed_worktree_files)

  case "$current" in
    main)
      [[ "$(git rev-parse HEAD)" == "$(git rev-parse origin/main)" ]] ||
        fail "local main is not exactly current origin/main"
      if [[ -n "$changed" ]]; then
        [[ "$changed" == "docs/CHANGELOG.md" ]] ||
          fail "before release preparation, only a reviewed docs/CHANGELOG.md entry may be dirty"
        grep -qE "^## \[$NEW\] " docs/CHANGELOG.md ||
          fail "dirty docs/CHANGELOG.md does not contain a reviewed [$NEW] entry"
      fi
      git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH" &&
        fail "local branch already exists without a matching remote PR: $RELEASE_BRANCH"
      assert_calendar_cut_day "release-branch materialization"
      git switch -c "$RELEASE_BRANCH" >/dev/null
      ;;
    "$RELEASE_BRANCH")
      if [[ -z "$changed" && "$(git rev-parse HEAD)" != "$(git rev-parse origin/main)" ]]; then
        base=$(git merge-base origin/main HEAD)
        assert_release_delta "$base" HEAD "$NEW"
        "$ROOT/scripts/check-dco.sh" "$base" HEAD >/dev/null ||
          fail "prepared local release branch contains a commit without its author's DCO sign-off"
        "$ROOT/scripts/check-claims.sh"
        "$ROOT/scripts/check-release-hygiene.sh"
        [[ "$PREPARE_ONLY" -eq 0 ]] || return 0
        assert_reviewed_base HEAD
        git push -u origin "$RELEASE_BRANCH"
        fetch_release_branch
        assert_release_branch
        return
      fi
      if [[ -n "$changed" ]]; then
        while IFS= read -r file; do
          printf '%s\n' "$EXPECTED_FILES" | grep -qxF "$file" ||
            fail "unexpected dirty file while resuming $RELEASE_BRANCH: $file"
        done <<<"$changed"
      fi
      ;;
    *)
      fail "start a release from main or $RELEASE_BRANCH (currently $current)"
      ;;
  esac

  "$ROOT/scripts/check-claims.sh"
  if release_version::is_calendar_v2 "$NEW"; then
    today=$(release_version::calendar_v2_iso_date "$NEW")
  elif release_version::is_calendar "$NEW"; then
    today=$(release_version::calendar_iso_date "$NEW")
  else
    today=$(release_version::vienna_iso_date)
  fi
  first_existing_heading=$(awk '/^## \[/{print; exit}' docs/CHANGELOG.md)
  second_existing_heading=$(awk '/^## \[/{headings++; if (headings == 2) {print; exit}}' docs/CHANGELOG.md)

  if [[ "$first_existing_heading" == "## [Unreleased]" ]]; then
    if [[ ! "$second_existing_heading" =~ ^##\ \[${PAIMOS_RELEASE_VERSION_ERE}\]\ —\ [0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
      fail "docs/CHANGELOG.md carries a duplicate or non-canonical leading [Unreleased] section"
    fi
    canonical_unreleased=1
  elif [[ "$first_existing_heading" == "## [Unreleased]"* ]]; then
    fail "docs/CHANGELOG.md carries a non-canonical leading [Unreleased] section"
  fi
  if grep -qE "^## \[$NEW\] " docs/CHANGELOG.md && \
     [[ ! "$second_existing_heading" =~ ^##\ \[${PAIMOS_RELEASE_VERSION_ERE}\]\ —\ [0-9]{4}-[0-9]{2}-[0-9]{2}$ ]]; then
    fail "reviewed [$NEW] entry must consume, not retain, the leading [Unreleased] section"
  fi

  if [[ $NO_EDIT -eq 1 ]] && ! grep -qE "^## \[$NEW\] " docs/CHANGELOG.md && [[ "$canonical_unreleased" -ne 1 ]]; then
    fail "non-interactive release requires a reviewed ## [$NEW] entry or one canonical ## [Unreleased] section"
  fi

  printf '%s\n' "$NEW" > VERSION

  tmp=$(mktemp)
  sed -E "s~<code>v${PAIMOS_RELEASE_VERSION_ERE}</code>~<code>v$NEW</code>~" README.md > "$tmp"
  mv "$tmp" README.md

  tmp=$(mktemp)
  sed -E \
    -e "s~VER=${PAIMOS_RELEASE_VERSION_ERE}~VER=$NEW~g" \
    -e "s~(paimos --version[[:space:]]+# )${PAIMOS_RELEASE_VERSION_ERE}~\1$NEW~" \
    docs/INSTALL.md > "$tmp"
  mv "$tmp" docs/INSTALL.md

  if grep -qE "^## \[$NEW\] " docs/CHANGELOG.md; then
    tmp=$(mktemp)
    sed -E "s|^## \[$NEW\] .*|## [$NEW] — $today|" docs/CHANGELOG.md > "$tmp"
    mv "$tmp" docs/CHANGELOG.md
  elif [[ "$canonical_unreleased" -eq 1 ]]; then
    tmp=$(mktemp)
    awk -v replacement="## [$NEW] — $today" \
      '!consumed && $0 == "## [Unreleased]" {$0=replacement; consumed=1} {print}' \
      docs/CHANGELOG.md > "$tmp"
    mv "$tmp" docs/CHANGELOG.md
  else
    tmp=$(mktemp)
    {
      awk 'BEGIN{p=1} /^## \[/{p=0} p' docs/CHANGELOG.md
      printf '## [%s] — %s\n\n' "$NEW" "$today"
      printf '### Added — TODO fill in before committing\n\n'
      git log "$LAST_TAG..origin/main" --format='- %s' -- backend/ frontend/src/ docs/ scripts/ || true
      printf '\n'
      awk 'f{print} /^## \[/{if(!f){f=1; print}}' docs/CHANGELOG.md
    } > "$tmp"
    mv "$tmp" docs/CHANGELOG.md
    echo "Opening CHANGELOG in \$EDITOR (${EDITOR:-vi}) for review…"
    "${EDITOR:-vi}" docs/CHANGELOG.md
  fi

  "$ROOT/scripts/check-release-hygiene.sh"
  changed=$(changed_worktree_files)
  assert_expected_file_set "$changed" "release preparation"

  git add VERSION README.md docs/CHANGELOG.md docs/INSTALL.md
  if git diff --cached --quiet; then
    fail "release preparation produced no commit"
  fi
  git commit --no-gpg-sign --signoff -m "release: $NEW_TAG"
  base=$(git merge-base origin/main HEAD)
  assert_release_delta "$base" HEAD "$NEW"
  [[ "$PREPARE_ONLY" -eq 0 ]] || return 0
  assert_reviewed_base HEAD
  if [[ "${RELEASE_TEST_FAILPOINT:-}" == "before-branch-push" ]]; then
    fail "injected interruption before release branch push"
  fi
  git push -u origin "$RELEASE_BRANCH"
  fetch_release_branch
  assert_release_branch
}

create_release_pr() {
  local body
  assert_reviewed_base "origin/$RELEASE_BRANCH"
  body=$(printf '%s\n' \
    "## Release $NEW_TAG" \
    "" \
    "Protected-main release prepared by scripts/release.sh." \
    "" \
    "- release commit carries the author's DCO sign-off" \
    "- release hygiene and claim gates passed locally" \
    "- tag will be created only at this PR's protected-main merge commit")
  gh pr create \
    --repo "$REPO" \
    --base main \
    --head "$RELEASE_BRANCH" \
    --title "release: $NEW_TAG" \
    --body "$body" >/dev/null
  echo "Opened protected release PR for $NEW_TAG."
}

cleanup_checkout() {
  local current local_oid main_worktree pr_head_oid remote_oid tag_oid
  current=$(git rev-parse --abbrev-ref HEAD)
  git fetch --quiet origin main
  if [[ "$current" == "$RELEASE_BRANCH" ]]; then
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "cannot clean up: release checkout is dirty"
    main_worktree=$(git worktree list --porcelain | awk '
      $1 == "worktree" { worktree = $2 }
      $1 == "branch" && $2 == "refs/heads/main" { print worktree; exit }
    ')
    if [[ -n "$main_worktree" ]]; then
      tag_oid=$(git rev-parse "$NEW_TAG^{commit}" 2>/dev/null) ||
        fail "cannot clean up: local $NEW_TAG does not resolve to a commit"
      [[ "$tag_oid" == "$TAG_OID" ]] ||
        fail "cannot clean up: local $NEW_TAG points to $tag_oid, expected $TAG_OID"
      git switch --detach "$TAG_OID" >/dev/null
      [[ "$(git rev-parse HEAD)" == "$TAG_OID" ]] ||
        fail "cannot clean up: detached checkout differs from $TAG_OID"
    else
      git switch main >/dev/null
    fi
  fi
  if [[ "$(git rev-parse --abbrev-ref HEAD)" == "main" ]]; then
    git merge --ff-only origin/main >/dev/null
  fi
  if git show-ref --verify --quiet "refs/heads/$RELEASE_BRANCH"; then
    local_oid=$(git rev-parse "$RELEASE_BRANCH")
    [[ "$local_oid" == "$VALIDATED_HEAD_OID" ]] ||
      fail "refusing to delete drifted local $RELEASE_BRANCH at $local_oid"
    git branch -D "$RELEASE_BRANCH" >/dev/null
  fi
  remote_oid=$(git ls-remote --heads origin "$RELEASE_BRANCH" | awk '{print $1}')
  if [[ -n "$remote_oid" ]]; then
    [[ "$remote_oid" == "$VALIDATED_HEAD_OID" ]] ||
      fail "refusing to delete drifted origin/$RELEASE_BRANCH at $remote_oid"
    git push origin --delete "$RELEASE_BRANCH" >/dev/null
  fi
  if git show-ref --verify --quiet "$PR_HEAD_REF"; then
    pr_head_oid=$(git rev-parse "$PR_HEAD_REF")
    [[ "$pr_head_oid" == "$VALIDATED_HEAD_OID" ]] ||
      fail "refusing to delete drifted PR evidence ref $PR_HEAD_REF at $pr_head_oid"
    git update-ref -d "$PR_HEAD_REF" "$VALIDATED_HEAD_OID"
  fi
}

for cmd in git gh jq cmp; do
  require_command "$cmd"
done

git fetch --tags --quiet origin
git fetch --quiet origin main
LAST_TAG=$(latest_release_tag)
[[ -n "$LAST_TAG" ]] || fail "no supported release tags yet — create v0.1.0 manually first"
LAST_VERSION="${LAST_TAG#v}"
LAST_KIND=$(release_version::kind "$LAST_VERSION")

if [[ -z "$MODE" ]]; then
  echo "Last release: $LAST_TAG"
  echo
  echo "All commits since $LAST_TAG:"
  git log "$LAST_TAG..origin/main" --oneline
  echo
  echo "Runtime-relevant (backend/ frontend/src/):"
  git log "$LAST_TAG..origin/main" --oneline -- backend/ frontend/src/ || echo "  (none)"
  echo
  echo "Re-run with: now | <YYMMDDhhmmss.0.0>   (INSPR calendar v2, UTC; legacy modes are closed)"
  exit 0
fi

EXISTING_RELEASE_TAGS=$(origin_release_tags)
# PAI-979 / INSPR-395: once a v2 coordinate is published, every older era is
# closed for new cuts. Legacy SemVer was already closed by the first v1 cut.
if release_version::has_calendar_v2_tag "$EXISTING_RELEASE_TAGS"; then
  CALENDAR_V2_ACTIVE=1
else
  CALENDAR_V2_ACTIVE=0
fi
case "$MODE" in
  now)
    # Reserve the current UTC second as the coordinate. It is used for the
    # tag, image, VERSION, changelog and every artifact of this release.
    NEW=$(release_version::utc_coordinate)
    release_version::calendar_v2_reservation_policy "$NEW" "$EXISTING_RELEASE_TAGS" ||
      fail "could not reserve UTC coordinate $NEW (a later v2 coordinate is already published?)"
    ;;
  patch|minor|major)
    [[ "$CALENDAR_V2_ACTIVE" -eq 0 ]] ||
      fail "legacy $MODE releases are closed after this product's first INSPR calendar v2 release; use: now"
    [[ "$LAST_KIND" == semver ]] ||
      fail "$MODE is available only before this product's first calendar release"
    IFS=. read -r LAST_MAJOR LAST_MINOR LAST_PATCH <<<"$LAST_VERSION"
    case "$MODE" in
      patch) NEW="$LAST_MAJOR.$LAST_MINOR.$((LAST_PATCH + 1))" ;;
      minor) NEW="$LAST_MAJOR.$((LAST_MINOR + 1)).0" ;;
      major) NEW="$((LAST_MAJOR + 1)).0.0" ;;
    esac
    ;;
  *)
    NEW="${MODE#v}"
    release_version::is_supported "$NEW" ||
      fail "mode must be now|<YYMMDDhhmmss.0.0> (or a legacy form before the first v2 cut); 6.0.0 is prohibited (got: $MODE)"
    if release_version::is_calendar_v2 "$NEW"; then
      # An explicit coordinate must have been reserved earlier the same UTC
      # day (for example by an external-stage publication that pins the
      # next release tag) and must still be later than every published one.
      release_version::calendar_v2_reservation_policy "$NEW" "$EXISTING_RELEASE_TAGS" ||
        fail "v2 coordinate must be today's UTC day, not in the future, and later than every published v2 release: $NEW"
    elif release_version::is_calendar "$NEW"; then
      [[ "$CALENDAR_V2_ACTIVE" -eq 0 ]] ||
        fail "calendar v1 (yy.mm.dd[.hh.mm]) releases are closed after this product's first INSPR calendar v2 release; use: now"
      release_version::calendar_recut_policy "$NEW" "$EXISTING_RELEASE_TAGS" ||
        fail "calendar release must use today's Vienna date; .hh.mm is valid only for a same-day recut"
      assert_origin_calendar_recut_evidence "$NEW" "$EXISTING_RELEASE_TAGS"
    else
      [[ "$CALENDAR_V2_ACTIVE" -eq 0 ]] ||
        fail "legacy SemVer releases are closed after this product's first INSPR calendar v2 release; use: now"
      [[ "$LAST_KIND" == semver ]] ||
        fail "legacy SemVer releases are closed after this product's first calendar release"
    fi
    ;;
esac
release_version::is_supported "$NEW" ||
  fail "computed release $NEW is prohibited; reserve the UTC coordinate with: now"
NEW_TAG="v$NEW"
RELEASE_BRANCH="release/$NEW_TAG"
assert_external_stage_release_pin origin/main "$NEW_TAG"
assert_external_stage_v2_release_pin origin/main "$NEW_TAG"
REPO=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
[[ -n "$REPO" ]] || fail "could not resolve GitHub repository"
CURRENT_BRANCH=$(git rev-parse --abbrev-ref HEAD)
case "$CURRENT_BRANCH" in
  main|"$RELEASE_BRANCH") ;;
  *) fail "start or resume $NEW_TAG from main or $RELEASE_BRANCH (currently $CURRENT_BRANCH)" ;;
esac

echo "Release target: $LAST_TAG → $NEW_TAG"
echo "Protected branch: $RELEASE_BRANCH"

PR_JSON=$(find_release_pr)
if [[ -n "$PR_JSON" ]]; then
  [[ -z "$(changed_worktree_files)" ]] ||
    fail "working tree is dirty while reusing the canonical release PR"
  validate_pr_identity "$PR_JSON"
  PR_NUMBER=$(printf '%s\n' "$PR_JSON" | jq -r '.number')
  PR_URL=$(printf '%s\n' "$PR_JSON" | jq -r '.url')
  PR_STATE=$(printf '%s\n' "$PR_JSON" | jq -r '.state')
  [[ "$PREPARE_ONLY" -eq 0 ]] || fail 'release already has a PR; prepare-only cannot modify a published release'
  if [[ -n "$REVIEWED_HEAD" ]]; then
    [[ "$(printf '%s\n' "$PR_JSON" | jq -r '.headRefOid')" == "$REVIEWED_HEAD" ]] ||
      fail "release PR differs from reviewed commit $REVIEWED_HEAD"
    [[ "$PR_STATE" != 'OPEN' ]] || assert_reviewed_base "$REVIEWED_HEAD"
  fi
  echo "Reusing release PR: $PR_URL ($PR_STATE)"
else
  if git rev-parse "$NEW_TAG" >/dev/null 2>&1; then
    fail "$NEW_TAG exists without its canonical $RELEASE_BRANCH PR"
  fi
  if [[ -n "$REVIEWED_HEAD" ]]; then
    [[ -z "$(changed_worktree_files)" ]] || fail 'reviewed release checkout is dirty'
    assert_reviewed_base HEAD
  fi
  if remote_branch_exists; then
    [[ "$PREPARE_ONLY" -eq 0 ]] || fail 'release branch already published; prepare-only is local only'
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "working tree is dirty while reusing origin/$RELEASE_BRANCH"
    fetch_release_branch
    assert_release_branch
  else
    prepare_release_branch
    if [[ "$PREPARE_ONLY" -eq 1 ]]; then
      prepared_release_receipt
      exit 0
    fi
  fi
  create_release_pr
  PR_JSON=$(find_release_pr)
  [[ -n "$PR_JSON" ]] || fail "release PR was not discoverable after creation"
  validate_pr_identity "$PR_JSON"
  PR_NUMBER=$(printf '%s\n' "$PR_JSON" | jq -r '.number')
  PR_URL=$(printf '%s\n' "$PR_JSON" | jq -r '.url')
  PR_STATE=$(printf '%s\n' "$PR_JSON" | jq -r '.state')
fi

case "$PR_STATE" in
  OPEN)
    [[ -z "$(changed_worktree_files)" ]] ||
      fail "working tree is dirty while release PR is open"
    fetch_release_branch
    assert_release_branch
    assert_open_pr_head_matches_remote "$PR_JSON"
    fetch_and_validate_pr_head "$PR_JSON"
    run_release_gates_at_ref "$PR_HEAD_REF"
    wait_for_protected_merge
    ;;
  MERGED)
    fetch_and_validate_pr_head "$PR_JSON"
    run_release_gates_at_ref "$PR_HEAD_REF"
    assert_merged_release_provenance "$PR_JSON"
    MERGE_OID=$(printf '%s\n' "$PR_JSON" | jq -r '.mergeCommit.oid // empty')
    [[ -n "$MERGE_OID" ]] || fail "merged release PR has no merge commit"
    ;;
  CLOSED)
    fail "release PR closed without merge: $PR_URL"
    ;;
  *)
    fail "unexpected release PR state: $PR_STATE"
    ;;
esac

assert_release_merge "$MERGE_OID"
select_release_tag_commit "$MERGE_OID"
if [[ -n "$REVIEWED_HEAD" ]]; then
  [[ "$(git rev-parse "$TAG_OID^{tree}")" == "$(git rev-parse "$REVIEWED_HEAD^{tree}")" ]] ||
    fail 'selected recovery commit changes reviewed content; prepare and review a fresh release'
fi
# Dispatch the full workflow for this immutable protected-main commit if no
# evidence exists. Only the operator waits; tag CI checks completed evidence
# once. Main may advance without changing the code verified or tagged.
GITHUB_REPOSITORY="$REPO" "$ROOT/scripts/wait-backend-full.sh" --dispatch "$TAG_OID"
tag_release_merge "$TAG_OID"
cleanup_checkout

echo
echo "Pushed $NEW_TAG from protected-main commit $TAG_OID."
echo "Waiting for ghcr.io/inspr-at/paimos:$NEW to appear…"
# shellcheck disable=SC1091
source "$ROOT/scripts/_deploy-lib.sh"
for _ in $(seq 1 60); do
  if ghcr::image_exists "ghcr.io/inspr-at/paimos:$NEW"; then
    echo "✔ ghcr.io/inspr-at/paimos:$NEW is live."
    break
  fi
  sleep 10
done
if ! ghcr::image_exists "ghcr.io/inspr-at/paimos:$NEW"; then
  fail "image is still unavailable after 10m; inspect tag CI before deploying"
fi

echo "Waiting for tag workflows to publish release evidence…"
"$ROOT/scripts/wait-release-ci.sh" "$NEW_TAG" --workflows ci,release

echo
echo "Next:"
echo "  just verify-release $NEW_TAG"
echo "  deploy ppm through the protected composeStack pin in docs/DEPLOY.md"
echo "  just doc-sync $NEW_TAG"
