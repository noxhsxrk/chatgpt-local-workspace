# Homebrew Core readiness

This document tracks the work needed before proposing `chatgpt-local-workspace` to `homebrew/core`. It is not an assertion that Homebrew will accept the package.

## Upstream requirements

- Publish the upstream source. The project uses the MIT license.
- Publish a stable, immutable version tag and a versioned source archive. A Homebrew formula can then use its HTTPS URL and SHA-256 checksum.
- Establish public interest and maintenance history under Homebrew's [package acceptance policy](https://docs.brew.sh/Package-Acceptance-Policy). Confirm the current repository metrics before submitting.
- Review the entire Git history and screenshots for private information before making the repository public.

## Packaging work

- Replace the current formula's SSH source URL and `openai/tools/tunnel-client` third-party tap dependency with an approach acceptable to `homebrew/core`. The application launcher no longer installs packages on its own. An independently installed `tunnel-client` is still needed for tunnel connections; verify with Homebrew maintainers whether that dependency arrangement is acceptable.
- Declare the chosen license in the formula and use the stable source archive and checksum.
- Make the supported macOS-only launcher scope explicit and verify whether that restriction and the app's optional web interface are acceptable for a core formula.
- Confirm `livecheck` can discover stable releases and that the formula's offline test exercises the installed server.
- Run `brew install --build-from-source`, `brew test`, `brew audit --strict --new --online`, and `brew style`, then inspect the installed files.
- Complete a security review, especially workspace access and writes, saved credentials, logging, and loopback server access.

Only open a `homebrew/core` pull request after these checks and the [formula acceptance requirements](https://docs.brew.sh/Acceptable-Formulae) have been reviewed. Disclose AI assistance as required by the Homebrew contribution process.
