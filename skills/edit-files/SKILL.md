---
name: edit-files
description: Reads, creates, and changes files in the shared workspace folder using small exact edits instead of rewriting whole files. Use it to fix, refactor, write, or review code, notes, and configs in the workspace.
---

# Edit Files

Make precise changes to files in `~/workspace`, the folder the user shares with
you through the LLM Factory terminal. It covers finding, reading, creating and
editing text files. It does not run code (see `run-and-test`), and it can't
reach files outside the terminal.

## When to Use

- "Fix the bug in ...", "rename ... everywhere", "add a section to ...".
- "Create a script, config, or README for ...".
- "What does this file do?", "review ...".

## Prerequisites

- The user selected the **LLM Factory workspace** terminal for this chat.
- The files live under `~/workspace` (a folder on the user's computer, shown on
  the factory's Harness page).

## How to Run

Find the file with `glob_search` or `grep_search`, read the relevant lines with
`read_file`, change them with `replace_file_content`, then read them back. Use
`write_file` only for new files or a full rewrite the user asked for.

## Quick Reference

| Tool | Use |
| --- | --- |
| `list_files` | List a folder (`directory`). |
| `glob_search` | Find files by name (`pattern`, e.g. `*.py`; `path`). |
| `grep_search` | Find text in files (`query`, `path`, `include`). |
| `read_file` | Read a file, optionally only `start_line`..`end_line`. |
| `replace_file_content` | Exact find-and-replace (`replacements`: `target`, `replacement`). |
| `write_file` | Create or overwrite a whole file (`path`, `content`). |
| `display_file` | Open a file in the user's file viewer. |

## Procedure

1. Locate the target with `glob_search` (name pattern) or `grep_search`
   (a symbol or phrase).
2. `read_file` the lines around the change (use `start_line`/`end_line` for
   big files) so your edit matches the current text exactly.
3. Call `replace_file_content` with a `target` copied verbatim from what you
   read, indentation included, and the new `replacement`. Put several changes
   to one file in one call. Set `allow_multiple` only for intended bulk renames.
4. For a new file, `write_file` the complete content; parent folders are
   created for you.
5. `read_file` the changed region again and check it says what you intended.
6. Tell the user what changed and where (file and line numbers). Use
   `display_file` if they want to see the result.

## Pitfalls

- `replace_file_content` fails when the target is missing or matches more than
  once: read the file again and use a longer, unique target.
- Don't retype a whole file to change a few lines; it silently drops content.
- `write_file` overwrites without asking. Check with `list_files` or
  `read_file` first when the file might already exist.
- Skills in `~/.agents/skills` are read-only. Copy one into `~/workspace` to
  change it.

## Verification

`read_file` on the changed lines shows the new text, and `grep_search` for the
old text finds no leftover matches (unless some were meant to stay).
