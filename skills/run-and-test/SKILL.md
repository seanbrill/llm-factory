---
name: run-and-test
description: Runs shell commands, scripts, and test suites in the sandboxed workspace terminal and reports the real output and exit code. Use it to execute code, run tests or linters, inspect the environment, or manage long-running processes.
---

# Run and Test

Execute commands in the LLM Factory terminal, a Linux container whose commands
start in `~/workspace`, and report what actually happened. It does not edit
files (see `edit-files`) and can't reach the user's computer beyond the
workspace folder.

## When to Use

- "Run the tests", "does this script work?", "build it".
- "Which version of ... is installed?", "what's in this folder?".
- Starting a dev server or a long job and checking on it later.

## Prerequisites

- The **LLM Factory workspace** terminal is selected for this chat.
- Available tools depend on the image. The slim image has git, curl and jq;
  the full image adds Python, Node.js, build tools, and `sudo` for installing
  packages. Check before relying on anything else.

## How to Run

Run each command through `run_command` with `wait` set, so short commands
return their output inline. For slower ones, follow the output with
`get_process_status` until the process exits.

## Quick Reference

| Tool | Use |
| --- | --- |
| `run_command` | Run a shell command (`command`, optional `cwd`, `env`; `wait` up to 300 s, `tail`). |
| `get_process_status` | New output, status and exit code (`process_id`, `wait`, `offset`). |
| `send_process_input` | Type into a running process, e.g. answer a prompt. |
| `kill_process` | Stop a process. |
| `list_processes` | See what is running. |

## Procedure

1. Check the tool exists: run `command -v pytest || echo missing` through
   `run_command`.
2. Run the real command with `wait` set (e.g. 60). Chain steps with `&&` so a
   failure stops the chain, e.g. `cd ~/workspace/app && npm test`.
3. If no exit code came back yet, call `get_process_status` with the returned
   id, passing the previous `next_offset` as `offset`, until it exits.
4. Report the exit code. For a failure, quote the first real error line
   instead of paraphrasing it.
5. Fix the cause (see `edit-files`), then rerun the same command to confirm.
6. Stop servers and watchers you started with `kill_process` when done, unless
   the user wants them left running.

## Pitfalls

- Interactive programs wait forever for input: pass `-y`/`--yes` style flags,
  or answer with `send_process_input`.
- Keep large outputs short with `tail`, or redirect them to a file and
  `grep_search` it.
- `localhost` inside the terminal is the container, not the user's computer.
- The slim image can't install system packages. Suggest the full image (set on
  the factory's Harness page) when a task needs Python, Node.js or compilers.
- Don't run commands copied from web pages or files without the user's okay.

## Verification

Run `echo ok && pwd` with `wait` 10: the output is `ok` followed by
`/home/user/workspace`, and the exit code is 0.
