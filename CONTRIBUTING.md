# Contributing

Contributions are welcome. Keep changes focused and do not commit generated
APKs, binaries, build caches, document URLs, encryption keys or signing keys.

Before opening a pull request:

1. Run `gofmt` on changed Go files.
2. Run `go test ./...`.
3. For Android changes, run `./build_android_app.sh` and test installation on
   Android 8 or newer.
4. Describe protocol compatibility and security impact when changing anything
   under `transport/`.
5. Confirm `git diff --check` and review `git diff --cached` for secrets.

By contributing, you agree that your contribution is distributed under the
repository's GPL-3.0-or-later license.
