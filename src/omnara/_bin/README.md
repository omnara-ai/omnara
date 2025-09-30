This directory contains prebuilt agent binaries that are bundled with the Python wheel.

Layout (by platform):

- codex/darwin-arm64/codex
- codex/darwin-x64/codex
- codex/linux-x64/codex
- codex/win-x64/codex.exe
- gemini/darwin-arm64/gemini (optional)
- gemini/darwin-x64/gemini (optional)
- gemini/linux-x64/gemini (optional)
- gemini/win-x64/gemini.exe (optional)

The `omnara` CLI resolves the appropriate binary at runtime. If no packaged binary
is present (e.g., in a development checkout), you can build it locally or specify a custom path.

## Building Locally

To build the codex binary in a local omnara repo:
```bash
cd src/integrations/cli_wrappers/codex/codex-rs && cargo build --release -p codex-cli
```

The built binary will be at: `src/integrations/cli_wrappers/codex/codex-rs/target/release/codex`

## Using a Custom Binary

Set the `OMNARA_CODEX_PATH` environment variable to specify a custom binary path.
This can be either:
- A direct path to the binary file
- A directory containing the binary (will look for `codex` or `codex.exe` based on platform)

For Gemini, the launcher resolves the `gemini` CLI similarly. You can either rely on a
packaged binary (if present), ensure `gemini` is on your `PATH`, or set `OMNARA_GEMINI_PATH`
to the binary file or a directory containing `gemini`/`gemini.exe`.
