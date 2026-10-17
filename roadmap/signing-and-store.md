# netdiag: code signing and Microsoft Store plan (as of 2026-10-17)

**Short answer.** These are two separate jobs, and the Store doesn't need the Azure certificate.
- **Azure Artifact Signing** (the new name for "Trusted Signing") signs the exe files in the GitHub zips. It costs $9.99 a month and takes about an hour of your time. You also have to pass an ID check, which usually clears the same day but can take up to 20 business days.
- **Microsoft Store** is free. You ship netdiag there as an **MSIX package**, and Microsoft signs it for you. netdiag needs roughly 3–5 days of code changes first.

---

## 1. Azure signing: name, cost, time

| | |
|---|---|
| **Name** | **Azure Artifact Signing.** It was renamed from Trusted Signing when it became generally available in January 2026. Accounts, billing and the `Microsoft.CodeSigning` resource provider didn't change, and some price meters and tools still say "Trusted Signing". |
| **Who can use it** | Individuals in the **US or Canada** only. You need a **paid** Azure subscription (pay-as-you-go); free, trial and sponsored ones are refused. The billing account's type must be **Individual**, and its legal name and address must match your photo ID exactly. |
| **Cost** | **Basic: $9.99/month** for 5,000 signatures. netdiag uses 2 per release. Billing starts the moment you create the account and is not prorated. Premium ($99.99) isn't needed. Prices come from the Azure Retail Prices API, checked 2026-10-17. |
| **Time** | About 1–1.5 hours hands-on. The ID check usually updates within minutes, but Microsoft quotes 1–20 business days. |
| **What it does** | Signs the exe with a certificate in your **legal name** (you can't customise it, so it may read "Donald Steele"). Windows then shows you as a verified publisher instead of "Unknown publisher", and Smart App Control lets the exe run. |
| **What it doesn't do** | The SmartScreen "unrecognized app" warning stays until reputation builds over weeks of clean downloads. It doesn't guarantee Defender won't flag a build. EV certificates no longer skip SmartScreen either, so don't buy one. |

## 2. Your steps, in order

Steps marked **(C)** are ones Claude can run for you with your approval, once you're signed in with `az login` and `gh auth login`.

1. **Pick your ID** (passport, driver's license or state ID). The first and last name on it become the certificate name. *5 min*
2. **Set up Azure billing.** Go to portal.azure.com and create a **pay-as-you-go** subscription with your personal Microsoft account. In Cost Management + Billing, check that Account Type = **Individual** and that the legal name and sold-to address match your ID character for character. If they don't, `az billing account update ... --sold-to ...` can fix them. *15–30 min*
3. **(C) Register the provider and add the CLI extension.**
   - `az provider register --namespace Microsoft.CodeSigning`
   - `az extension add --name artifact-signing`
4. **(C) Create the signing account.** Do this only when you can finish step 6 the same day, because billing starts here.
   - Resource group `rg-netdiag-signing`.
   - Artifact Signing account on the **Basic** tier in **East US**, which gives the endpoint `https://eus.codesigning.azure.net`.
   - The account name must be 3–24 letters or digits, start with a letter, and be unique across Azure.
5. **Give yourself two roles** on the account (Access control (IAM) > Add role assignment):
   - **Artifact Signing Identity Verifier**
   - **Artifact Signing Certificate Profile Signer**

   Being subscription Owner is not enough to sign.
6. **Do the identity check.** This can only be done in the portal.
   1. Open Identity validations, switch to **Individual**, then New Identity > **Public**.
   2. Pick your billing account and click Create.
   3. When the status shows "Action Required", open the link. Sign in with the same email you entered.
   4. Choose the AU10TIX option: enter the email PIN and your phone number, then photograph your ID and do the face check. Use no VPN and no flash.
   5. Add the Verified ID to Microsoft Authenticator, then press Share.

   *20–40 min of your time.*
7. **(C) Create a "Public Trust" certificate profile**, for example `netdiagPublic`. Do **not** use "Public Trust Test", because its signatures stop working after 3 days. Write down the exact subject name shown in the preview.
8. **(C) Give GitHub Actions permission to sign, without a password.** This uses OIDC (GitHub proves its identity to Azure directly, so no secret is stored anywhere).
   - Create an Entra app and its service principal.
   - Add a federated credential with subject **`repo:donaldsteele@8108109/netdiag@1413460805:environment:release`**.
   - Give that app the **Certificate Profile Signer** role on the certificate profile only.

   I checked today that this repo uses GitHub's newer "immutable" subject format (`gh api .../actions/oidc/customization/sub` returns `use_immutable_subject: true`). The portal's GitHub template would generate `repo:donaldsteele/netdiag:...`, which won't match.
9. **(C) Set up the GitHub release environment.**
   - Create an environment called `release`. Add yourself as a required reviewer and allow only `v*` tags.
   - Secrets: `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`, `AZURE_SUBSCRIPTION_ID`.
   - Variables: `ARTIFACT_SIGNING_ENDPOINT`, `ARTIFACT_SIGNING_ACCOUNT`, `ARTIFACT_SIGNING_PROFILE`.

**What to send Claude.** None of these are passwords; they are only identifiers.

| Item | Example |
|---|---|
| Subscription ID and Tenant ID | GUIDs |
| Resource group | `rg-netdiag-signing` |
| Signing account name | your chosen name |
| Region and endpoint | East US, `https://eus.codesigning.azure.net` |
| Certificate profile name | `netdiagPublic` |
| Exact certificate subject (CN) | e.g. `Donald Steele` |
| CI app (client) ID, if you created it yourself | GUID |

**If OIDC can't be used,** the fallback is a service principal client secret (a password for the app). Set it yourself with `gh secret set AZURE_CLIENT_SECRET --env release` and paste it at the prompt. Never put it in chat or in a file. Give it a short expiry (6–12 months) and set a reminder to renew it. OIDC is the better option.

**Ongoing upkeep:**
- $9.99/month.
- The identity check has to be renewed. You can start from 60 days before it expires, and Microsoft emails reminders. If it lapses, signing stops.
- Deleting the account stops billing. Files you already signed stay valid.

## 3. What Claude will build

- **`cmd/netdiag/winres/winres.json`**: change `CompanyName` ("Don Steele" today) and `LegalCopyright` ("Copyright (c) 2026 Don Steele") to the exact certificate name, so users see one name everywhere.
- **`scripts/build.ps1` signing step:**
  - Signs the windows/amd64 and windows/arm64 exes **after `go build` and before zipping**.
  - Uses SignTool 10.0.2261.755 or newer with the Artifact Signing plugin, configured from the environment variables above.
  - Every signature is timestamped. This is required, because the certificates themselves only last 72 hours.
  - If signing isn't configured, the step prints a warning and skips, so PR and normal CI builds don't change. A `-RequireSigning` switch makes a release build fail instead.
  - Checksums are computed after signing. `build.sh` (macOS) stays unsigned.
- **Verification:** `signtool verify /pa /v /all`, plus `Get-AuthenticodeSignature` must report Valid with a timestamp. If either check fails, the build fails.
- **New `.github/workflows/release.yml`:**
  - Runs when you push a `v*` tag, on a windows-2025 runner, in the `release` environment.
  - Signs in to Azure with OIDC (`azure/login@v3`), then signs with `azure/artifact-signing-action@v2` or `build.ps1 -RequireSigning`, then verifies.
  - Uploads the zips to a **draft** GitHub release, which you check and publish.
  - Third-party actions are pinned to exact commits. `ci.yml` is unchanged, and PRs and forks never get signing access.
- **Optional:** local signing on your PC (`winget install -e --id Microsoft.Azure.ArtifactSigningClientTools`), and a README note on how to check the signature.
- **Effort:** about half a day.

**First signed release check:**
- Download a zip from GitHub in a browser and extract it.
- Check Properties > Digital Signatures on both exes.
- Expect SmartScreen "unrecognized" prompts for a few weeks while reputation builds.

## 4. Microsoft Store

**Recommended route: MSIX**, with winget added later as an extra.

| Route | Verdict |
|---|---|
| **MSIX** | **Recommended.** Free. Microsoft signs it, hosts it and updates it automatically. Users get a Start menu entry, a clean uninstall and no SmartScreen warnings. One `.msixbundle` file covers both x64 and arm64. |
| EXE/MSI installer | **No.** It would need a new silent, offline installer and a versioned download URL that you host, and the Store won't update installed copies. Every exe must also be signed by a CA in Microsoft's Trusted Root Program, and Microsoft's own table lists Artifact Signing as **not eligible**. |
| winget | **Extra only.** `winget install --source msstore <StoreID>` works for free once netdiag is in the Store. A winget listing for the zips is also possible, but it is command-line only and adds no Start menu shortcut. |

**What netdiag must change first** (Claude, about 3–5 days):
1. **No console window.**
   - Add a second build using `-H=windowsgui` (e.g. `netdiag-app.exe`) for the Start menu tile.
   - The current console `netdiag.exe` stays available through a command alias, so `netdiag` and `netdiag mcp` still work in a terminal.
   - Errors that used to print to the console are shown in the browser or a message box instead.
2. **Single instance and a clear way to quit.**
   - A second click on the tile reopens the running copy instead of starting another one.
   - The existing Quit button stays.
   - Add either auto-exit when no browser tab is open, or a tray icon (your choice, see section 7).
3. **Version 1.0.0.** MSIX versions can't start with 0, and the fourth part must be 0, so 0.5.x isn't allowed. Use 1.0.0.0, and move the GitHub releases to 1.0.0 as well so the two match.
4. **Privacy policy page** at `docs/privacy.html` (required by Store policy 10.5.1). It covers: no telemetry, data kept locally, what LAN and Wi-Fi details it reads, and the connectivity check to `connectivitycheck.gstatic.com`.
5. **MSIX packaging.**
   - A manifest that runs netdiag as a full desktop app, with the `runFullTrust` capability, the command alias and location access for the Wi-Fi panel.
   - Icon assets.
   - `scripts/package-msix.ps1` to build the `.msixbundle`, plus a CI step.
6. **Testing.** Test on clean x64 and Arm64 machines, then run the Windows App Certification Kit. Things to check:
   - Inside MSIX, `%AppData%` is moved to a private folder that is deleted on uninstall, so the Store copy and the zip copy won't share history.
   - The Wi-Fi panel's location prompt.
   - Whether the firewall prompts during the local network scan.
   - That Remote Desktop still opens.
7. **Defender:** submit the new exes at microsoft.com/wdsi/filesubmission as "Software developer" before submitting to the Store. The Store's first certification test is a malware scan, and Defender has flagged netdiag before.

Already Store-friendly: native Windows APIs, no admin rights, data in the user's config folder, and the existing 2560x1600 screenshots.

**Your Partner Center steps:**
1. **Create a free Individual developer account** at storedeveloper.microsoft.com with your personal Microsoft account. You scan your ID and take a selfie; there's no fee and no card (free since 10 Sep 2025). This ID check is separate from Azure's.
2. **Reserve the name.** Go to Apps and games > New product > MSIX or PWA app, enter "netdiag", then Check availability > Reserve.
   - Have a fallback name ready: "netdiag" may be taken, or may be confused with Microsoft's old NetDiag tool.
   - A reserved name is released if nothing is submitted within 3 months.
3. **Send Claude the package identity.** Copy **Package/Identity/Name**, **Publisher (CN=...)** and **PublisherDisplayName** exactly from Product management > Product identity.
4. **Fill in the submission.** Claude can draft all of the text.
   - Free, all markets; category Utilities + tools.
   - Privacy URL, website and support link (GitHub issues).
   - Age rating questionnaire.
   - Upload the `.msixbundle` and explain why it needs `runFullTrust`.
   - Listing: plain-text description with no URLs, and screenshots.
   - Notes for testers: the UI opens in the browser on 127.0.0.1, the Quit button stops it, and a test machine may show few devices.
5. **Submit.** Certification takes up to 3 business days, and the app goes live about 15 minutes after it passes. Then add a "Get it from Microsoft" badge to the site and README. Each update needs a higher version and a new submission.

## 5. Mac equivalent

- **Your part:** join the Apple Developer Program as an individual for **$99/year**. Create a "Developer ID Application" certificate and an App Store Connect API key for notarization (Apple's automated malware check).
- **Claude's part:** on the existing macOS CI runner, sign with `codesign --force --options runtime --timestamp`, zip, then run `xcrun notarytool submit --wait`. After that, remove the `xattr` workaround from the README.
- **Limit:** a zip or bare binary can't carry the notarization ticket, so the first launch needs internet. A `.pkg` or `.dmg` avoids that if it matters.
- Artifact Signing doesn't help on Mac.

## 6. Recommended order and timeline

| When | You | Claude |
|---|---|---|
| Day 1 | Steps 1–2 (Azure billing); create the Partner Center account; make the decisions in section 7 | Build the `build.ps1` signing step (skips when not configured) and `release.yml` |
| Same sitting | Steps 4–7 (signing account, roles, ID check, certificate profile) | Run the OIDC and GitHub environment setup (steps 3, 8, 9) with your approval |
| Once the ID check passes (usually same day) | Approve the release | First signed release, then verify it |
| Weeks 1–2 | Reserve the Store name and send the package identity | Windowless launcher, single instance, privacy page, v1.0.0, MSIX packaging, tests, WDSI submission |
| Weeks 2–3 | Fill in and submit the Store listing | Listing text and tester notes |
| +3 business days | Store listing is live | Badge on the site and README |
| Later | Apple Developer ($99/yr) | Mac notarization; winget listing |

**Totals:** about 2–3 weeks elapsed if the ID check is quick. Running cost is $9.99/month, plus $99/year if you add Apple. If you want only the Store, you can skip Azure, but people who download the GitHub zips will keep seeing "Unknown publisher".

## 7. Open questions and uncertainties

**Decisions for you:**
- Both channels (signed zips and the Store), or the Store only?
- Tray icon or auto-exit? A tray icon keeps monitoring and alerts running with the tab closed. It also needs a tray library that builds for windows/arm64 without C code (CGO off), which still has to be checked.
- Move every release to v1.0.0?
- A fallback Store name.

**Uncertain or not yet verified:**
- **Artifact Signing for the Store's EXE/MSI route** is marked "not eligible" in Microsoft's code-signing options table (2026-08-29). The certificate-management page (2026-09-30) says its certificate authorities left the Trusted Root Program but are still trusted by Windows. There's also one third-party report of a rejection, but no explicit announcement. None of this affects the MSIX plan.
- **Certificate name:** it is your legal ID name and can't be customised. Your city, state and country also appear in every signed file. How long an identity check stays valid isn't documented, so read the expiry date on the portal page.
- **SmartScreen:** Microsoft publishes no threshold beyond "several weeks and hundreds of clean installs". Users on Microsoft Q&A reported warnings coming back after Microsoft rotated its intermediate certificates in 2026.
- **Defender:** signing should reduce false-positive flags but won't prevent them, and Microsoft has no allowlist for developers.
- **Certificate lifetime:** Microsoft's docs say 72 hours, while DevClass said 24 hours. Either way, timestamping is mandatory and will be built in.
- **MSIX details nobody documents:**
  - How the Wi-Fi location permission behaves inside a packaged app; it has to be tested.
  - The approval criteria for `runFullTrust`. Community reports say approval is routine for desktop apps.
  - Whether the exe inside a Store-signed MSIX counts as signed for Defender. Claude will sign the inner exes anyway, which is harmless.
- **MSIX tools and rules:** `winapp` CLI is still a preview, so MakeAppx from the Windows SDK is the fallback. The "first part can't be 0" version rule comes from the MSIX package requirements page.
- **Tool versions:** the release years of `artifact-signing-action` v2.0.0 ("14 May") and `azure/login` v3.1.0 ("10 Sep") are assumed to be 2026. Claude will re-check them before pinning to exact commits. Install paths from the winget client tools aren't verified, so `build.ps1` will find them itself or take them from environment variables.
- **Review times:** Apple enrollment time and winget review times aren't published.

**Sources** (dates are Microsoft's page dates or when they were fetched):
- Artifact Signing docs, learn.microsoft.com/azure/artifact-signing/:
  - quickstart (updated 2026-10-08)
  - faq (2026-10-06)
  - how-to-signing-integrations (2026-08-03)
  - concept-certificate-management (2026-09-30)
  - tutorial-assign-roles (2026-08-03)
  - how-to-renew-identity-validation (2026-09-23)
- Azure Retail Prices API (queried 2026-10-17); azure.microsoft.com/pricing/details/artifact-signing (fetched 2026-10-17)
- Windows app docs, learn.microsoft.com/windows/apps/:
  - package-and-deploy/code-signing-options (2026-08-29)
  - package-and-deploy/smartscreen-reputation (2026-08-17)
  - publish/store-policies (v7.20, 2026-09-14)
  - publish/.../msix/app-package-requirements, .../msi/app-package-requirements and .../msix/app-certification-process (all 2026-08-24)
- Free individual Store registration: blogs.windows.com/windowsdeveloper/2025/09/10/free-developer-registration-for-individual-developers-on-microsoft-store/
- Immutable OIDC subjects: learn.microsoft.com/entra/workload-id/workload-identities-github-immutable-subjects (2026-07-28). The repo values were confirmed by `gh api` on 2026-10-17.
- Signing tools: github.com/Azure/artifact-signing-action and github.com/Azure/login releases (fetched 2026-10-17)
- Defender: learn.microsoft.com/defender-xdr/developer-faq (2026-09-10)
- Smart App Control: support.microsoft.com "What is Smart App Control" (fetched 2026-10-17)
- Apple: developer.apple.com/documentation/security/notarizing-macos-software-before-distribution (fetched 2026-10-17)
- GA and rename reporting: DevClass, 2026-01-14
- SmartScreen warnings after the certificate rotation: Microsoft Q&A thread 5954185 (Jul–Sep 2026)
- Repo files read: `C:\code\netDiscovery\cmd\netdiag\winres\winres.json`, `C:\code\netDiscovery\scripts\build.ps1`, `C:\code\netDiscovery\.github\workflows\ci.yml` (the only workflow today), `C:\code\netDiscovery\go.mod`