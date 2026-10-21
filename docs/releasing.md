# Releasing

Releases are cut from a Mac, because the macOS binaries are signed with the Developer ID certificate and
notarized by Apple, and both the certificate and the notary API key stay on the release owner's machine.
GitHub Actions checks every push and pull request and can rebuild the Linux and Windows archives, but it
never sees the signing secrets.

## What CI checks

`.github/workflows/ci.yml` runs on every push to `main` and every pull request to `main`, with no secrets:

- `gofmt -l`, `go vet ./...` and `go test -race ./...` on Linux; `go vet` and `go test ./...` on Windows;
- `scripts/build-release.sh` with `UNSIGNED=1` for all five platforms, then `shasum -c SHA256SUMS`;
- `bash -n` on every script in `scripts/`;
- `python3 -m unittest discover -s scripts` when `scripts/test_*.py` files exist (skipped otherwise);
- `scripts/check-public-safety.sh`.

The Go version comes from `go.mod`; modules are cached by `actions/setup-go`.

## One-time setup on the release Mac

- Go (the version in `go.mod`), `gh` logged in with write access to the repository, `rg` (ripgrep).
- For the Homebrew tap step: `python3`, and `git push` rights to the tap repository over the URL the
  script uses (`https://github.com/Alexandr-Kravchuk/homebrew-tap.git` by default). `gh auth status`
  alone does not make `git push` over HTTPS work; run `gh auth setup-git` once, or set
  `HOMEBREW_TAP_URL` to an SSH URL such as `git@github.com:Alexandr-Kravchuk/homebrew-tap.git`.
- The signing secrets file `~/secrets/adac-release.env` (another path can be given in `RELEASE_ENV`). It
  exports `MAC_CERT_P12` (path to the Developer ID Application `.p12`), `MAC_CERT_PASSWORD`,
  `APPLE_API_KEY` (path to the App Store Connect `.p8` key), `APPLE_API_KEY_ID` and `APPLE_API_ISSUER`.
  It must stay outside the repository; the scripts never print it.

## Cutting a release

1. Merge everything into `main` and wait for CI to pass.
2. Write the release notes into a file outside the repository (or in a path that is not tracked), for
   example `~/notes-v0.2.0.md`.
3. Rehearse:

   ```bash
   scripts/release.sh --dry-run v0.2.0 ~/notes-v0.2.0.md
   ```

   The dry run checks the tag, the notes file, `gh` login, a clean tree, gofmt, vet, tests and the
   public-safety check, builds all archives without signing, and verifies `dist/SHA256SUMS`. Off `main`
   or behind `origin/main` it only warns. With `DRY_RUN_SIGNED=1` it also signs and notarizes, which
   needs the secrets file and takes a few minutes. When the Homebrew tap is reachable, it also prints the
   formula it would push (with the hashes of the local unsigned build, not of the published archives).
4. Release:

   ```bash
   scripts/release.sh --title "v0.2.0 — short description" v0.2.0 ~/notes-v0.2.0.md
   ```

   The script refuses to run unless the tree is clean, the branch is `main` and `HEAD` equals
   `origin/main`, and neither the tag nor the release exists yet. It then:
   - runs gofmt, `go vet`, `go test` and the public-safety check;
   - builds and signs the archives with `scripts/build-release.sh` (signing and notarization included);
   - verifies `dist/`: all five archives present, `SHA256SUMS` matches, and on macOS both darwin binaries
     pass `spctl -a -t install` with `source=Notarized Developer ID`;
   - creates the annotated tag and pushes it;
   - creates the GitHub release as a pre-release with the notes and uploads the archives and
     `SHA256SUMS`;
   - downloads the published release back, compares its `SHA256SUMS` with the local one byte for byte,
     and repeats the hash and notarization checks on the downloaded files;
   - updates the Homebrew tap (see below), unless it is skipped.

   If it stops after the push, fix the cause and finish by hand with `gh release create` (or
   `gh release upload --clobber`), then download and verify as above. Do not delete a pushed tag that
   anyone may have fetched.
5. Promote the pre-release to a full release on GitHub when testers confirm it.

Why `spctl -a -t install`: for a bare command-line binary `spctl -a -t execute` always answers
"does not seem to be an app", even when it is notarized. The install assessment reports the source, and
only `source=Notarized Developer ID` proves notarization rather than just a valid signature.

## Homebrew tap

The tap [`Alexandr-Kravchuk/homebrew-tap`](https://github.com/Alexandr-Kravchuk/homebrew-tap) holds one
formula, `Formula/creatio-mcp-go.rb`, installed with `brew install alexandr-kravchuk/tap/creatio-mcp-go`.
The formula does not build from source: for macOS (arm, Intel) and Linux (arm64, x86_64) it points at the
release archive and its sha256 from the release's `SHA256SUMS`, installs the binary, checks it with
`creatio-mcp-go --version`, and prints the `claude mcp add` / `codex mcp add` commands with the `opt`
path, which stays the same across upgrades.

- `scripts/homebrew-formula.py --version <tag> [--sums <file or URL>] --out <dir> [--readme]` writes
  `<dir>/Formula/creatio-mcp-go.rb` (and the tap `README.md`). Without `--sums` it reads `SHA256SUMS`
  of the published release. It refuses a `SHA256SUMS` that lacks any of the four macOS and Linux
  archives. The formula has no `version` line: Homebrew takes the version from the archive name,
  pre-release suffixes included, and `brew audit --strict` rejects a repeated one.
- `scripts/update-homebrew-tap.sh [--dry-run] <tag> <SHA256SUMS>` clones the tap, regenerates the
  formula and README, commits `creatio-mcp-go <version>` and pushes. If the tap already has this
  version it pushes nothing. Exit code 3 means the tap is not reachable or has no commits yet.
- `scripts/release.sh` runs it as the last step, after the published release is verified, with
  `dist/SHA256SUMS` (just compared byte for byte with the published one). The tap is checked in the
  preconditions, before the tag: if it is not reachable, or `python3` is missing, the script warns and
  the release goes on without the tap. If the tap update fails after the release is published, the
  script fails with the retry command; run `scripts/update-homebrew-tap.sh <tag> dist/SHA256SUMS` once
  the cause is fixed.
- Options and variables of `scripts/release.sh`: `--no-homebrew` skips the tap.
  `--homebrew-prerelease` (the default) updates the tap for a pre-release too, because the project
  ships only pre-releases so far; `--no-homebrew-prerelease` leaves the tap to full releases, and since
  `scripts/release.sh` always publishes a pre-release, it currently means the tap is not updated.
  `HOMEBREW_TAP_REPO` (default `Alexandr-Kravchuk/homebrew-tap`) and `HOMEBREW_TAP_URL` (default
  `https://github.com/$HOMEBREW_TAP_REPO.git`) choose the tap.
- A release published another way (for example through the workflow below) does not update the tap:
  run `scripts/update-homebrew-tap.sh <tag> <downloaded SHA256SUMS>` after it is published.

To check a formula by hand, copy it into a local tap (`brew tap-new --no-git <user>/local-test`, then
into `$(brew --repository <user>/local-test)/Formula/`) and run `brew style`,
`brew audit --strict --online --formula`, `brew install`, `brew test`, then `brew uninstall` and
`brew untap`.

## Rebuilding Linux and Windows archives on GitHub

`.github/workflows/release.yml` (Actions → "Release (Linux and Windows archives)" → Run workflow, input
`tag`) builds the Linux and Windows archives from the tagged commit on a GitHub runner and attaches them
to an existing **draft** release. Use it when the Linux or Windows archives should come from a clean
runner instead of the release Mac:

1. Push the tag, create the release as a draft, and upload the signed macOS archives with their
   `SHA256SUMS` (for example from `TARGETS="darwin/arm64 darwin/amd64" scripts/build-release.sh <tag>`).
2. Run the workflow for the tag. It refuses a tag that is not pushed and a release that is not a draft,
   runs `go vet` and `go test`, builds with
   `UNSIGNED=1 TARGETS="linux/amd64 linux/arm64 windows/amd64" scripts/build-release.sh`, merges its
   lines into the draft's `SHA256SUMS` (the macOS lines stay), uploads with `--clobber`, downloads the
   files back and checks them.
3. Download the draft and run `shasum -a 256 -c SHA256SUMS` and the `spctl` check, then publish it.

The workflow needs no secrets; the job token with `contents: write` is enough. macOS archives are not
built there because signing and notarization need the Developer ID certificate and the notary key. Moving
them to CI would mean storing the `.p12`, its password and the `.p8` key as repository secrets; until that
is decided they stay local.

## Archive layout

`scripts/build-release.sh <tag>` writes to `dist/`:

- `creatio-mcp-go_<version>_<os>_<arch>.tar.gz` for darwin and linux, `.zip` for windows, where
  `<version>` is the tag without the leading `v`; each holds the binary, `LICENSE`, `README.md` and
  `.env.example` in a folder of the same name;
- `SHA256SUMS` in `shasum -a 256` format, one line per archive.

`UNSIGNED=1` skips signing (local tests only; never publish such a build). `TARGETS="<os>/<arch> ..."`
limits the platforms; without a darwin target no signing happens.
