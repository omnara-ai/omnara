This directory contains prebuilt agent binaries that are bundled with the Python wheel.

Layout (by platform):

- codex/darwin-arm64/codex
- codex/darwin-x64/codex
- codex/linux-x64/codex
- codex/win-x64/codex.exe

The `omnara` CLI resolves the appropriate binary at runtime. If no packaged binary
is present (e.g., in a development checkout), it falls back to the dev build at:

integrations/cli_wrappers/codex/codex-rs/target/release/codex

