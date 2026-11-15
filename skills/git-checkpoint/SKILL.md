---
name: git-checkpoint
description: Snapshots work in the workspace with git before risky changes, shows exactly what changed, and rolls back edits that went wrong. Use it before multi-file edits, when the user asks what changed, or to undo a change.
---

# Git Checkpoint

Use git inside `~/workspace` as a safety net: commit a checkpoint before you
change things, show the user a diff afterwards, and restore files when an edit
went wrong. It works on local repositories only; it doesn't push, pull, or
handle credentials.

## When to Use

- Before editing several files, refactoring, or running a formatter.
- "What did you change?", "show me the diff".
- "Undo that", "put it back the way it was".

## Prerequisites

- The **LLM Factory workspace** terminal is selected (every image has git).
- A project folder under `~/workspace` (each project can be its own repo).

## How to Run

Run git through `run_command` with `wait` set. Always pass `-C <folder>` so it
acts on the right project, and give commits a local identity so they never
stop to ask for one.

## Quick Reference

| Goal | Command (through `run_command`) |
| --- | --- |
| Is it a repo? | `git -C ~/workspace/app rev-parse --is-inside-work-tree` |
| Start tracking | `git -C ~/workspace/app init -q` |
| Checkpoint | `git -C ~/workspace/app add -A && git -C ~/workspace/app -c user.name=assistant -c user.email=assistant@localhost commit -qm "checkpoint: <why>"` |
| What changed | `git -C ~/workspace/app status --short` then `git -C ~/workspace/app diff` |
| Undo one file | `git -C ~/workspace/app restore <file>` |
| History | `git -C ~/workspace/app log --oneline -n 10` |

## Procedure

1. Check whether the project is a repository. If it isn't, ask the user before
   running `git init` (it adds a hidden `.git` folder to their project).
2. If `status --short` lists uncommitted work, commit a checkpoint first, so
   the user's own changes stay separate from yours.
3. Make your edits.
4. Show the result with `status --short` and `diff` (add `--stat` for a summary
   of large changes).
5. If something went wrong, restore single files with `restore <file>`, or
   return to the checkpoint with `reset --hard <commit>` once the user agrees.
6. If the user wants the finished work committed, commit it with a message
   that says what changed and why.

## Pitfalls

- `reset --hard` and `clean -fd` delete uncommitted work for good. Ask first.
- If git reports "detected dubious ownership", the folder belongs to the host
  user: run `git config --global --add safe.directory <folder>` once.
- Never commit secrets such as `.env` files or keys; add them to `.gitignore`.
- Leave large binary files untracked; they bloat the repository.
- Don't rewrite history the user has shared (no `rebase` or `push --force`).

## Verification

After a checkpoint, `git -C <folder> log --oneline -n 1` shows the new commit
and `git -C <folder> status --short` prints nothing.
