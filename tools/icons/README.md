# tools/icons (dev-only)

Builds `web/icons.svg`, the SVG symbol sprite used by the UI, from the
[lucide-static](https://www.npmjs.com/package/lucide-static) package. Also
renders the Windows exe icon (see "App icon" below).

This is never needed at build or run time: the generated sprite and the
license text (`web/vendor/LICENSE-lucide.txt`) are committed.

## Regenerate

```sh
cd tools/icons
npm ci
node build-sprite.mjs
```

- `package.json` pins `lucide-static` to the exact version `1.55.0`; `package-lock.json` is committed.
- `icons.txt` is the sprite manifest. Each non-comment line is `<sprite-id> <lucide-name>`,
  for example `i-check circle-check`; `#` starts a comment. The Lucide name must exist in the
  pinned package. Missing, renamed or duplicate ids make the build fail loudly.
- The sprite has a budget of 40 KB (40960 bytes); the script enforces this.
- Licence: Lucide is ISC-licensed. The licence text is committed as `web/vendor/LICENSE-lucide.txt`.
- After upgrading the pin, update the version line in `web/vendor/VERSIONS.txt`, and refresh the licence copy if it changed.

## App icon

`build-appicon.mjs` renders `docs/assets/favicon.svg` to
`cmd/netdiag/winres/icon-<size>.png` (16 to 256 px), which `scripts/build.*`
embed in `netdiag.exe` with the version info and manifest from
`cmd/netdiag/winres/winres.json` (via `go tool go-winres`). The PNGs are
committed; re-run only after changing the favicon. It uses the Playwright
Chromium from `tools/uitest`:

```sh
(cd tools/uitest && npm ci && npx playwright install chromium)
node tools/icons/build-appicon.mjs
```
