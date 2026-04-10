Run the local managed ralphex update check.

1. Execute `~/.local/bin/ralphex-check-update`.
2. Parse the returned JSON.
3. If `status=up_to_date`, post a short status update and stop.
4. If `status=error`, post the explicit error and stop.
5. If `status=update_available`:
   - record the current installed version and candidate version
   - clone `g-agent-lab/ralphex` into a temp directory, fetch tags, and inspect the diff between the installed custom tag and the candidate custom tag
   - review the changed files directly, focusing on updater scripts, automation files, release scripts, and any source changes under `cmd/` and `pkg/`
   - produce a verdict object:
     - `verdict: approve | block`
     - `summary`
     - `blockers[]`
   - post a concise human-readable result with:
     - current version
     - candidate version
     - approve/block
     - short summary
     - `run ~/.local/bin/ralphex-install-update` only if approved

Do not install updates automatically.
