# Capture a platform CLI response as a payload for dev-docs and fixtures. Owner's machine only.
#
#   pwsh scripts/cli/capture.ps1 <name> <noun> <verb> [args...]
#   pwsh scripts/cli/capture.ps1 routers network-routers list -m 5
#   pwsh scripts/cli/capture.ps1 router-add network-routers add ... -d
#
# Reads (list/get/types) run normally. Any other verb is refused unless it is a dry run (-d), which
# prints the request without sending it: nothing is ever created or changed (owner's rule).
# Output (-j -V) goes to $env:MTNCLOUD_CAPTURE_DIR (default: ./.captures, git-ignored). It holds
# real tenant data: summarise it with summarize.py, and scrub it before anything is committed.
#
# No param() block on purpose: PowerShell would match CLI flags such as -c or -N against named
# parameters by prefix. Everything after <name> goes to the CLI untouched.
$ErrorActionPreference = 'Stop'
if ($args.Count -lt 3) { throw 'usage: capture.ps1 <name> <noun> <verb> [args...]' }
$name = $args[0]
$cliArgs = @($args[1..($args.Count - 1)])
$readVerbs = @('list', 'get', 'types', 'type', 'get-type', 'list-types', 'security-groups', 'list-ips', 'routes')
$isRead = ($cliArgs[0] -eq 'whoami') -or ($readVerbs -contains $cliArgs[1])
$isDryRun = ($cliArgs -contains '-d') -or ($cliArgs -contains '--dry-run')
if (-not $isRead -and -not $isDryRun) {
    throw "refused: '$($cliArgs[0]) $($cliArgs[1])' would write. Add -d (dry run)."
}
$dir = if ($env:MTNCLOUD_CAPTURE_DIR) { $env:MTNCLOUD_CAPTURE_DIR } else { Join-Path (Get-Location) '.captures' }
New-Item -ItemType Directory -Force $dir | Out-Null
$out = Join-Path $dir "$name.txt"
& morpheus @cliArgs -j -V 2>&1 | Out-File -Encoding utf8 $out # mtncloud:allow-upstream-name (CLI binary name)
'{0,-28} {1,10} bytes  {2}' -f $name, (Get-Item $out).Length, $out
