This directory contains prebuilt agent binaries that are bundled with the Python wheel.

Layout (by platform):

- codex/darwin-arm64/codex
- codex/darwin-x64/codex
- codex/linux-x64/codex
- codex/win-x64/codex.exe

The `omnara` CLI resolves the appropriate binary at runtime. If no packaged binary
is present (e.g., in a development checkout), you can build it locally or specify a custom path.

## Building Locally

To build the codex binary in a local omnara repo:
```bash
cd integrations/cli_wrappers/codex/codex-rs && cargo build --release -p codex-cli
```

The built binary will be at: `integrations/cli_wrappers/codex/codex-rs/target/release/codex`

## Using a Custom Binary

Set the `OMNARA_CODEX_PATH` environment variable to specify a custom binary path.
This can be either:
- A direct path to the binary file
- A directory containing the binary (will look for `codex` or `codex.exe` based on platform)

