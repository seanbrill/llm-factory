# Dot-sourced by the start scripts to stamp the factory build with its version,
# so the UI (sidebar) and /api/version show exactly which code is running.
#   $FactoryVersion  VERSION file's major.minor + commit count (every commit to
#                    main bumps it), e.g. 1.0.153
#   $FactoryCommit   short SHA, with "-dirty" when tracked files have local edits
#   $FactoryLdflags  the matching `go build -ldflags` value
$vroot = (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path
$vbase = '0.0'
$vfile = Join-Path $vroot 'VERSION'
if (Test-Path $vfile) { $vbase = (Get-Content $vfile -Raw).Trim() }
$vcount = '0'
$FactoryCommit = 'unknown'
# try/catch: under $ErrorActionPreference = "Stop" (start-native.ps1), Windows
# PowerShell turns any git stderr (e.g. a safe.directory warning) into a
# terminating error; a version lookup must never abort the build.
if (Get-Command git -ErrorAction SilentlyContinue) {
    try {
        $c = (& git -C $vroot rev-list --count HEAD 2>$null | Select-Object -First 1)
        if ($c) { $vcount = "$c".Trim() }
        $s = (& git -C $vroot rev-parse --short HEAD 2>$null | Select-Object -First 1)
        if ($s) { $FactoryCommit = "$s".Trim() }
        if (& git -C $vroot status --porcelain --untracked-files=no 2>$null) { $FactoryCommit += '-dirty' }
    } catch { }
}
$FactoryVersion = "$vbase.$vcount"
$vpkg = 'github.com/seanbrill/local-llm/internal/version'
$FactoryLdflags = "-X $vpkg.Version=$FactoryVersion -X $vpkg.Commit=$FactoryCommit"
