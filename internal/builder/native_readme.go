package builder

import (
	"fmt"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/agent"
	"github.com/seanbrill/local-llm/internal/catalog"
	"github.com/seanbrill/local-llm/internal/native"
	"github.com/seanbrill/local-llm/internal/version"
)

type readmeInfo struct {
	m            catalog.Model
	t            native.Target
	man          *native.Manifest
	eng          native.Engine
	notes        packageNotices
	toolsets     []string
	hcfg         *agent.Config
	prompt       bool
	weightsBytes int64
	fromImage    string
	created      time.Time
}

type script struct{ name, body string }

// startScript is the file people double-click (macOS, Windows) or run (Linux).
//
// On macOS it is a .command file: Finder opens it in Terminal, which shows the
// log and stops the model when the window closes. An .app bundle would need
// the whole bundle signed (a Go-signed program inside an unsigned bundle reads
// as "damaged" once downloaded), which the factory can't do without Apple's
// tools. A browser marks what it downloads as quarantined, and macOS then
// refuses unsigned programs; once the person has allowed the script (System
// Settings, Privacy & Security, Open Anyway), it clears that mark from the
// package folder so the launcher and llama.cpp can run.
func startScript(t native.Target, model string) script {
	switch t.OS {
	case "macos":
		return script{name: "Start.command", body: `#!/bin/sh
# Starts ` + model + ` on this Mac: double-click this file in Finder.
# Terminal opens and shows the model's log; your browser opens the chat page.
# Close the Terminal window (or press Ctrl-C in it) to stop the model.
cd "$(dirname "$0")" || exit 1
# A downloaded package carries macOS's quarantine flag, and macOS won't run
# its programs (they aren't signed by an Apple developer ID). You allowed this
# script to run, so clear the flag from the rest of the package folder.
if xattr -p com.apple.quarantine bin/llmpack >/dev/null 2>&1 \
   || xattr -p com.apple.quarantine bin/llama/llama-server >/dev/null 2>&1; then
  echo "Clearing the download quarantine flag from this package folder..."
  xattr -dr com.apple.quarantine . 2>/dev/null || true
fi
exec ./bin/llmpack "$@"
`}
	case "windows":
		return script{name: "Start.bat", body: strings.ReplaceAll(`@echo off
rem Starts `+model+`: double-click this file.
rem A console window shows the model's log; your browser opens the chat page.
rem Close the window (or press Ctrl-C in it) to stop the model.
cd /d "%~dp0"
"bin\llmpack.exe" %*
if errorlevel 1 pause
`, "\n", "\r\n")}
	default:
		return script{name: "start.sh", body: `#!/bin/sh
# Starts ` + model + `: run ./start.sh in a terminal (or double-click it, if
# your file manager runs scripts). Your browser opens the chat page.
# Press Ctrl-C to stop the model.
cd "$(dirname "$0")" || exit 1
exec ./bin/llmpack "$@"
`}
	}
}

func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// packageReadme is README.txt: how to run the package, what's in it, and the
// licenses.
func packageReadme(r readmeInfo) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	title := fmt.Sprintf("%s: native package for %s", r.m.Name, r.t.Label)
	w("%s\n%s\n\n", title, strings.Repeat("=", len([]rune(title))))
	w("This folder runs %s on this computer with llama.cpp %s (%s), without\n", r.m.Name, native.LlamaCppRelease, native.LlamaCppBuild)
	w("Docker or Podman. ")
	switch r.t.Accel {
	case native.AccelMetal:
		w("It uses the Mac's GPU through Metal, with every layer of the\nmodel on the GPU.\n")
	case native.AccelVulkan:
		w("It uses the GPU through Vulkan when the computer has a driver\nfor it, and the CPU otherwise.\n")
	default:
		w("It runs on the CPU.\n")
	}
	w("\nMade by LLM Factory %s on %s", version.Get().Version, r.created.Format("2 January 2006"))
	if r.fromImage != "" {
		w(", with the settings of the image %s", r.fromImage)
	}
	w(".\nNeeds: %s\n\n", r.t.Requirements)

	w("START IT\n--------\n")
	switch r.t.OS {
	case "macos":
		w(`Double-click "Start.command". Terminal opens and shows the model's log, and
your browser opens the chat page. Close the Terminal window, or press Ctrl-C
in it, to stop the model.

The first time on a Mac that downloaded this package, macOS says it can't
check "Start.command" for malicious software, because the package isn't
signed with an Apple Developer ID. To open it anyway:
  1. Click Done (or Cancel) in that message.
  2. Open System Settings, Privacy & Security, scroll down, and click
     "Open Anyway" next to "Start.command". Confirm with your password.
  3. Double-click "Start.command" again.
Start.command then clears the download flag from this folder, so the
launcher and llama.cpp run too. Or, in Terminal, in this folder:
  xattr -dr com.apple.quarantine .
  ./Start.command
A package you open straight from the factory's packages/ folder (not
downloaded through a browser) doesn't ask.
`)
	case "windows":
		w(`Double-click "Start.bat". A console window shows the model's log, and your
browser opens the chat page. Close the window, or press Ctrl-C in it, to stop
the model.

Unzip the whole package first (right-click the zip, Extract All); it doesn't
run from inside the zip. Windows SmartScreen may say it "protected your PC",
because the launcher isn't signed: click "More info", then "Run anyway". To
avoid that for every file, right-click the zip before extracting it,
Properties, tick "Unblock", OK.
`)
	default:
		w(`In a terminal, in this folder:
  ./start.sh
Your browser opens the chat page. Press Ctrl-C to stop the model.
Unzip with a tool that keeps file permissions and links (unzip, or your file
manager); if ./start.sh says "permission denied", run: chmod +x start.sh bin/llmpack bin/llama/llama-server
`)
	}

	w("\nThe chat page is at http://127.0.0.1:8080 (if port 8080 is taken, the next\nfree port: the log says which).\n")

	w("\nUSE IT FROM OTHER PROGRAMS\n--------------------------\n")
	w(`The model serves an OpenAI-compatible API, with no key, on this computer
only: base URL http://127.0.0.1:8080/v1. For example:
  curl http://127.0.0.1:8080/v1/chat/completions \
    -H "Content-Type: application/json" \
    -d '{"messages":[{"role":"user","content":"Hello"}]}'
Web pages on other sites can't call it from a browser unless you allow them
with --allow-origin.
`)

	w("\nOPTIONS\n-------\n")
	launcher := "./bin/llmpack"
	if r.t.OS == "windows" {
		launcher = `bin\llmpack.exe`
	}
	w("Pass them to the start script or run the launcher yourself (%s --help):\n", launcher)
	w(`  --port 9000          another port (default 8080)
  --ctx-size 16384     a bigger or smaller context window (this package: %d tokens)
  --gpu-layers auto    model layers on the GPU: all, auto (as many as fit) or a
                       number; 0 runs on the CPU (this package: %s)
  --no-browser         don't open the chat page
  --no-harness         the plain model, without its tools
  --workspace DIR      another folder for the model's files
  --quiet              write llama-server's log to llama-server.log
  --verify             check every file against SHA256SUMS
  -- ARGS              anything after -- goes to llama-server as it is
`, r.eng.CtxSize, r.eng.GPULayers)

	w("\nMEMORY AND SPEED\n----------------\n")
	w("The model file is %s. Running it takes about that much memory plus the\n", gb(r.weightsBytes))
	w("context's (it grows with --ctx-size).")
	if r.t.Accel == native.AccelMetal {
		w(` macOS lets the GPU use about two-thirds of
the Mac's memory (three-quarters on Macs with more than 36 GB). If the model
doesn't fit, start it with a smaller --ctx-size, or --gpu-layers auto to keep
some layers on the CPU (slower).
`)
	} else {
		w(` With a GPU, --gpu-layers auto puts as many
layers on it as its memory holds and runs the rest on the CPU.
`)
	}

	w("\nTHE AGENT HARNESS\n-----------------\n")
	if r.hcfg == nil {
		w("This package has no harness: the model answers on its own, without tools.\n")
	} else {
		on := r.hcfg.EnabledTools()
		w("The model can use tools while it answers: %s.\n", strings.Join(r.toolsets, ", "))
		if on[agent.ToolListFiles] || on[agent.ToolWriteFile] || on[agent.ToolReadFile] {
			w(`Its file tools read and write only the "workspace" folder in this package
(or the folder --workspace names). They refuse paths outside it.
`)
		}
		if on[agent.ToolSearchWeb] || on[agent.ToolFetchURL] {
			w(`Web search and page reading go out to the internet; page reading refuses
addresses on this computer and your local network.
`)
		}
		w(`There is no shell tool: outside a container nothing would keep commands
away from the rest of this computer. Start with --no-harness for the plain
model, or set HARNESS_TOOLS (for example HARNESS_TOOLS=-web_search) to change
the tools for one run. HARNESS_BRAVE_API_KEY or HARNESS_SEARXNG_URL pick
another search backend.
`)
	}
	if r.prompt {
		mode := "when a client sends no system message of its own"
		if r.man.InjectMode == "always" {
			mode = "always, in place of any a client sends"
		}
		w("\nThe package adds an initialization prompt (config/system_prompt.txt) %s.\n", mode)
	}

	w("\nWHAT'S IN THIS FOLDER\n---------------------\n")
	rows := [][2]string{
		{startScript(r.t, "").name, "starts the model"},
		{native.ReadmeName, "this file"},
		{native.ManifestName, "what the launcher runs: the model, its settings, the engine"},
		{native.ChecksumsName, "the SHA-256 of every file"},
		{"bin/" + r.t.LauncherName(), "the launcher (LLM Factory's llmpack)"},
		{"bin/llama/", fmt.Sprintf("llama.cpp %s (%s): llama-server and its libraries", native.LlamaCppRelease, native.LlamaCppBuild)},
		{r.eng.Model, "the model weights"},
	}
	if r.eng.MMProj != "" {
		rows = append(rows, [2]string{r.eng.MMProj, "the vision projector (lets the model see images)"})
	}
	if r.prompt {
		rows = append(rows, [2]string{"config/system_prompt.txt", "the initialization prompt"})
	}
	if r.hcfg != nil {
		rows = append(rows, [2]string{"harness/harness.json", "the agent harness: tools and limits"})
		if r.man.SkillsDir != "" {
			rows = append(rows, [2]string{r.man.SkillsDir + "/", "skills the model can read"})
		}
		rows = append(rows, [2]string{native.WorkspaceDir + "/", "the model's files"})
	}
	rows = append(rows, [2]string{"licenses/", "the licenses of the model and llama.cpp"})
	for _, row := range rows {
		w("  %-34s %s\n", row[0], row[1])
	}
	w("\nCheck the files (after unzipping):\n")
	switch r.t.OS {
	case "windows":
		w("  bin\\llmpack.exe --verify\n")
	case "macos":
		w("  shasum -a 256 -c SHA256SUMS      (or ./bin/llmpack --verify)\n")
	default:
		w("  sha256sum -c SHA256SUMS          (or ./bin/llmpack --verify)\n")
	}

	w("\nLICENSES\n--------\n")
	switch {
	case r.notes.License != "":
		w("The model: %s", r.notes.License)
		if r.notes.LicenseName != "" {
			w(" (%s)", r.notes.LicenseName)
		}
		w(".")
	default:
		w("The model: its license couldn't be read when the package was made")
		if r.notes.Why != "" {
			w(" (%s)", r.notes.Why)
		}
		w(".")
	}
	if r.notes.SourceURL != "" {
		w(" Source: %s", r.notes.SourceURL)
	}
	w("\n")
	if r.notes.LicenseLink != "" {
		w("License text: %s\n", r.notes.LicenseLink)
	}
	if len(r.notes.Files) > 0 {
		w("Its license files and model card are in licenses/.\n")
	}
	if !r.notes.Shareable {
		w("Read the model's license before you give this package to anyone else.\n")
	}
	w("llama.cpp: MIT License, Copyright (c) 2023-2026 The ggml authors (licenses/llama.cpp/).\n")
	w("The launcher is part of LLM Factory.\n")
	return b.String()
}

// llamaCppLicense is llama.cpp's license, for a release archive that carries
// no copy (the Windows ones).
const llamaCppLicense = `MIT License

Copyright (c) 2023-2026 The ggml authors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
`
