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
   needs the secrets file and takes a few minutes.
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
     and repeats the hash and notarization checks on the downloaded files.

   If it stops after the push, fix the cause and finish by hand with `gh release create` (or
   `gh release upload --clobber`), then download and verify as above. Do not delete a pushed tag that
   anyone may have fetched.
5. Promote the pre-release to a full release on GitHub when testers confirm it.

Why `spctl -a -t install`: for a bare command-line binary `spctl -a -t execute` always answers
"does not seem to be an app", even when it is notarized. The install assessment reports the source, and
only `source=Notarized Developer ID` proves notarization rather than just a valid signature.

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
