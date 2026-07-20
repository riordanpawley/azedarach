#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
clone_root="$(mktemp -d "${TMPDIR:-/tmp}/azedarach-migration-clones.XXXXXX")"
manifest="$clone_root/manifest.tsv"
cleanup() {
  rm -rf -- "$clone_root"
}
trap cleanup EXIT
chmod 700 "$clone_root"

for dependency in az jq sqlite3; do
  if ! command -v "$dependency" >/dev/null 2>&1; then
    echo "required command not found: $dependency" >&2
    exit 1
  fi
done

backup_database() {
  local authority="$1"
  local source="$2"
  local clone="$3"
  if [[ ! -f "$source" ]]; then
    printf 'absent\t%s\t%s\t-\n' "$authority" "$source" >>"$manifest"
    return
  fi
  sqlite3 "$source" ".backup '$clone'"
  chmod 600 "$clone"
  printf 'cloned\t%s\t%s\t%s\n' "$authority" "$source" "$clone" >>"$manifest"
}

user_db_source="${HOME}/.azedarach/azedarach.db"
user_db_clone="$clone_root/user-azedarach.db"
backup_database user "$user_db_source" "$user_db_clone"

projects_json="$clone_root/projects.json"
az project list --json >"$projects_json"
project_clones=()
while IFS=$'\t' read -r project_id project_path; do
  [[ -n "$project_id" && -n "$project_path" ]] || continue
  project_source="$project_path/.azedarach/azedarach.db"
  project_clone="$clone_root/project-$project_id.db"
  backup_database "project:$project_id" "$project_source" "$project_clone"
  if [[ -f "$project_clone" ]]; then
    project_clones+=("$project_clone")
  fi
done < <(jq -r '.projects[] | [.id, .path] | @tsv' "$projects_json")

printf 'Migration clone manifest (temporary; source paths only, no row data):\n'
sed 's/^/  /' "$manifest"

validation_env=()
if [[ -f "$user_db_clone" ]]; then
  validation_env+=("AZEDARACH_USER_DB_CLONE=$user_db_clone")
fi
if ((${#project_clones[@]})); then
  project_clone_list="$(IFS=:; printf '%s' "${project_clones[*]}")"
  validation_env+=("AZEDARACH_PROJECT_DB_CLONES=$project_clone_list")
fi

env "${validation_env[@]}" just --justfile "$repo_root/justfile" --working-directory "$repo_root" test-timing migration-clone
