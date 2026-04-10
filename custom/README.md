# Custom Ralphex Layer

This fork carries a reproducible customization layer for `g-agent-lab`:

- source patches under `custom/patches/`
- embedded default overrides under `custom/overrides/`
- local updater/install scripts under `custom/local-bin/`
- automation and release helpers under `custom/scripts/`

Release flow:

1. `sync-upstream.yml` checks for a new upstream tag
2. it creates a `sync/<upstream-tag>` branch from the upstream tag
3. it restores `custom/`, applies patches/overrides, runs verification, builds assets
4. it pushes a custom tag `vX.Y.Z-gurgen.N`
5. it publishes a GitHub release with:
   - darwin arm64 binary archive
   - local tools archive
   - runtime overrides archive
   - checksums
   - `release-metadata.json`

Local install flow:

- `~/.local/bin/ralphex-check-update`
- `~/.local/bin/ralphex-install-update`

The managed binary lives in `~/.local/bin/ralphex` and is the only supported update target.
