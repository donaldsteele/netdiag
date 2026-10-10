# netdiag UI harness (tools/uitest)

Dev-only Playwright + axe-core harness for the embedded UI in `web/`. Nothing here
is embedded in netdiag or needed to build or run it. `node_modules/` and `out/` are
git-ignored. Node is only needed to run this harness; `go build` and `go test` do not use it.

## Setup and run

```
cd tools/uitest
npm ci
npx playwright install chromium
npx playwright test
```

Useful subsets (the `--reporter=line` flag keeps the output short):

```
npx playwright test tests/flow.spec.mjs          # scan, history, settings, live flow
npx playwright test tests/responsive.spec.mjs    # 360 to 1440 px, no horizontal page scroll
npx playwright test tests/motion.spec.mjs        # reduced-motion emulation
npx playwright test tests/docs-shots.spec.mjs    # README screenshots (see below)
npm run shots                                    # shots.spec.mjs only (name filter)
```

Outputs (all under `out/`, git-ignored):
- `out/shots/<theme>-<width>x<height>.png`: dark and light, at 1440x900 and 390x844 (full page).
- `out/shots/{empty,health,history,settings}-<theme>.png`: state screenshots.
- `out/shots/vision-<type>-<theme>-{devices,health}.png` and `out/axe/vision-<type>-<theme>.json`: colour-vision screenshots from `vision.spec.mjs`.
- `out/axe/<config>.json`: axe violations per screenshot config (also attached to the test report).
- `out/docs/devices-dark.png` and `out/docs/drawer-light.png`: the two README screenshots.
- `out/test-results/`: Playwright artifacts.

## Refreshing the README screenshots

1. Run `npx playwright test tests/docs-shots.spec.mjs`. The spec renders a 1280x800 viewport
   at CSS scale 1 with reduced motion emulated, so the PNGs stay under 250 KB each.
2. Check the sizes with `ls -l out/docs/`.
3. Copy the two files into the repo: `cp out/docs/devices-dark.png out/docs/drawer-light.png ../../docs/img/`.
4. Check that `README.md` still references `docs/img/devices-dark.png` and `docs/img/drawer-light.png`.

Do not add the copies to the harness output folder by hand; `docs/img/` is the committed location.
The copy into `docs/img/` is manual: the spec writes only to `out/docs/`, and nothing copies the files for you.

## How it works

- `support/mock.mjs` intercepts every request with `page.route`. Files under `web/` are
  served from disk with the right MIME types, including `font/woff2`. `/api/*` is
  mocked from `fixtures/`. Any request to another origin is aborted and recorded.
  Any unknown route is recorded as unmatched. Both lists must be empty in the flow tests.
- POST and PUT requests without `Content-Type: application/json` get a 415, the same
  as `internal/server/guard.go`.
- SSE: the mock fulfills `/api/scans/{id}/events` with `text/event-stream`. The body is
  built by `fixtures/index.mjs` `sseBody()` as `event: <type>\ndata: <json>\n\n`
  frames, with a leading `retry: 600000` so Chromium does not reconnect to the finite stream.
- Scenarios: `scan` runs stage, progress, 254 host events, conflict, notice, health,
  then done. `live` sends a snapshot of 253 hosts, then a new host, a host going
  offline with presence, a conflict, a notice and health, and never sends done.
- `support/config.mjs` holds `AXE_ENFORCE`, the single axe gate switch. It is `true`,
  so serious and critical axe violations fail the run. Set it to `false` only while
  diagnosing a layout regression, and never commit it that way.

## Files

- `fixtures/index.mjs`: shapes follow `internal/model` and the handlers. Values are
  synthetic and scrubbed.
- `support/config.mjs`: paths, base URL, `AXE_ENFORCE`, themes and viewports.
- `support/mock.mjs`: route mocks, static serving and `initStorage()` for
  `netdiag.theme` and `netdiag.live`.
- `support/axe.mjs`: `runAxe()` for WCAG 2.0 and 2.1 A and AA tags.
- `support/steps.mjs`: `scanToFinish()`, which presses Scan and waits for 254 rows.
- `tests/flow.spec.mjs`: flow smoke. Scan, hosts, history, compare, settings save and
  theme toggle, plus a live flow with new device, offline, conflict and notice.
- `tests/drawer.spec.mjs`, `tiles.spec.mjs`, `activity.spec.mjs`, `views.spec.mjs`:
  the drawer, tiles and health strip, activity log and navigation.
- `tests/responsive.spec.mjs`, `motion.spec.mjs`: width and reduced-motion checks.
- `tests/shots.spec.mjs`: screenshots and axe for the four configs.
- `tests/docs-shots.spec.mjs`: the two README screenshots.
- `tests/icons.spec.mjs`: icon presence in the sprite, the icon for each device type, and the device type in the drawer.
- `tests/vision.spec.mjs`: colour-vision check. Uses CDP `Emulation.setEmulatedVisionDeficiency` for deuteranopia, protanopia and tritanopia, in both themes, and writes the `out/shots/vision-*` files above.

## Stable hooks used by the specs

Ids: `#hosts`, `#history`, `#tab-history`, `#tab-devices`, `#nav-settings`,
`#settings-dialog`, `#alerts`, `#drawer`, `#log`, `#ticker`, `#tile-*`, `#hp-*`.
Classes: `section.progress .stage`, `.tiles`, `.hstrip`, `.livepill`, `.themebtn`,
`tr.host` (`.isnew`, `.selected`), `button.rowbtn[data-ip]`, `.badge.new|offline|conflict`,
`div.sr[role=status]`.
Labels: "Target (CIDR or range)", "Search hosts by IP, name, vendor or MAC", the buttons
Scan, Stop live, Compare, Settings and Save.

## Adding a new spec

Reuse `installMocks()` and `initStorage()`. Add fixture data in `fixtures/index.mjs`
using the server's JSON keys. Do not add absolute external URLs to `web/`.
