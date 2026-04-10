#!/bin/bash
export PATH="/opt/homebrew/opt/node@22/bin:/opt/homebrew/bin:$PATH"
exec /opt/homebrew/bin/codex "$@"
