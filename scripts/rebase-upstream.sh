#!/usr/bin/env bash
# Rebase the sphyrix fork branch onto upstream main; abort-safe, prints conflicts.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
branch="${1:-sphyrix}"
git fetch upstream --tags
git checkout -q "$branch"
if ! git rebase upstream/main; then
  echo "Rebase stopped; conflicting files:" >&2
  git diff --name-only --diff-filter=U >&2
  echo "Resolve, then: git rebase --continue   (or: git rebase --abort)" >&2
  exit 1
fi
git checkout -q main && git merge -q --ff-only upstream/main && git checkout -q "$branch"
echo "$branch is now on top of upstream/main ($(git rev-parse --short upstream/main))"
