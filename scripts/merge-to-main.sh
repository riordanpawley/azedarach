#!/usr/bin/env bash
set -euo pipefail

source_branch="${1:-}"
session_id="${2:-bhh}"

if [[ -z "${source_branch}" ]]; then
	source_branch="$(git rev-parse --abbrev-ref HEAD)"
fi

if [[ "${source_branch}" == "HEAD" ]]; then
	echo "ERROR: detached HEAD; pass an explicit source branch."
	echo "NEXT:"
	echo "1. git branch --show-current (confirm you're detached)"
	echo "2. choose a real branch name to merge"
	echo "3. rerun: just mtm <source-branch> [session-id]"
	exit 2
fi

if ! git rev-parse --verify "${source_branch}" >/dev/null 2>&1; then
	echo "ERROR: source branch '${source_branch}' does not exist in this repo."
	echo "NEXT:"
	echo "1. git branch --list"
	echo "2. pick the correct source branch name"
	echo "3. rerun: just mtm <source-branch> [session-id]"
	exit 2
fi

repo_root="$(git rev-parse --show-toplevel)"
main_worktree="$(
	git worktree list --porcelain | awk '
		$1=="worktree" { wt=$2 }
		$1=="branch" && $2=="refs/heads/main" { print wt; exit }
	'
)"

if [[ -z "${main_worktree}" ]]; then
	if [[ "$(git rev-parse --abbrev-ref HEAD)" == "main" ]]; then
		main_worktree="${repo_root}"
	else
		echo "ERROR: no worktree has branch 'main' checked out."
		echo "NEXT:"
		echo "1. create a main worktree, for example: git worktree add ../azedarach-main main"
		echo "2. verify with: git worktree list"
		echo "3. rerun: just mtm ${source_branch} ${session_id}"
		exit 2
	fi
fi

echo "merge-to-main"
echo "repo: ${repo_root}"
echo "source branch: ${source_branch}"
echo "main worktree: ${main_worktree}"
echo "session to kill: ${session_id}"

if [[ "$(git -C "${main_worktree}" rev-parse --abbrev-ref HEAD)" != "main" ]]; then
	echo "ERROR: '${main_worktree}' is not on branch 'main'."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. git checkout main"
	echo "3. rerun: just mtm ${source_branch} ${session_id}"
	exit 2
fi

if git -C "${main_worktree}" rev-parse -q --verify MERGE_HEAD >/dev/null; then
	echo "ERROR: main worktree already has an in-progress merge."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. git status --short"
	echo "3. resolve and commit that merge, or abort with: git merge --abort"
	echo "4. if committed, run: az session kill ${session_id} (no need to rerun mtm)"
	echo "5. if aborted, rerun: just mtm ${source_branch} ${session_id}"
	exit 2
fi

if [[ -n "$(git -C "${main_worktree}" status --porcelain)" ]]; then
	echo "ERROR: main worktree is not clean before merge."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. git status --short"
	echo "3. commit or stash changes until status is clean"
	echo "4. rerun: just mtm ${source_branch} ${session_id}"
	exit 2
fi

set +e
merge_output="$(git -C "${main_worktree}" merge --no-ff "${source_branch}" 2>&1)"
merge_code=$?
set -e
echo "${merge_output}"

if [[ ${merge_code} -ne 0 ]]; then
	echo "ERROR: merge failed."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. git status --short"
	echo "3. resolve conflicts and git add resolved files"
	echo "4. git commit to finish the merge"
	echo "5. az session kill ${session_id}"
	echo "6. verify cleanliness with: git status --short"
	exit "${merge_code}"
fi

if [[ -n "$(git -C "${main_worktree}" status --porcelain)" ]]; then
	echo "ERROR: merge succeeded but main worktree is still not clean."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. git status --short"
	echo "3. commit or stash the remaining changes until status is clean"
	echo "4. run: az session kill ${session_id}"
	exit 2
fi

echo "main worktree is clean after merge."

set +e
kill_output="$(
	cd "${main_worktree}" && az session kill "${session_id}" 2>&1
)"
kill_code=$?
set -e
echo "${kill_output}"

if [[ ${kill_code} -ne 0 ]]; then
	echo "ERROR: failed to kill session '${session_id}'."
	echo "NEXT:"
	echo "1. cd '${main_worktree}'"
	echo "2. az session list | rg '${session_id}' || true"
	echo "3. if it still exists, rerun: az session kill ${session_id}"
	echo "4. if it does not exist, stop here (merge already succeeded and main is clean)"
	exit "${kill_code}"
fi

echo "SUCCESS: merge complete, main is clean, session '${session_id}' killed."
