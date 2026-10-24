# Interface references

The implementation uses local installation metadata and the public interfaces below. It does not copy code from third-party update tools.

- [Homebrew manual](https://docs.brew.sh/Manpage): definitions of Cellar, keg, opt and Caskroom.
- [Homebrew Formulae API](https://formulae.brew.sh/docs/api/): version metadata by formula/cask identity.
- [Apple Search API](https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/Searching.html) and [Lookup examples](https://developer.apple.com/library/archive/documentation/AudioVideo/Conceptual/iTuneSearchAPI/LookupExamples.html): results also require product-identity and Mac-platform checks.
- [npm registry documentation](https://github.com/npm/registry/blob/main/docs/REGISTRY-API.md): public registry results are only candidates for packages whose source is unverified.
- [PyPI JSON API](https://docs.pypi.org/api/json/): package metadata; only info is streamed, and an oversized info object still produces an explicit error.
- [Official Go downloads](https://go.dev/dl/): the development compiler comes from official distributions, verified against the download manifest's SHA-256.

Installation formats and APIs can change. Failed parsing remains unknown or an error; it does not fall back to matching by name.

- [Sparkle publishing format](https://sparkle-project.org/documentation/publishing/): build, short version, default channel, macOS and hardware limits.
- [Chrome VersionHistory](https://developer.chrome.com/docs/web-platform/versionhistory/reference): version lookup and ordering by platform and channel.
- [VS Code update implementation](https://github.com/microsoft/vscode/blob/main/src/vs/platform/update/electron-main/updateService.darwin.ts): Darwin update service. Cursor uses the official update domain specified by its installed product.json.
- [Obsidian releases](https://github.com/obsidianmd/obsidian-releases): desktop version metadata. Firefox uses [Mozilla product metadata](https://product-details.mozilla.org/1.0/firefox_versions.json).
- [Python dependency specifiers](https://packaging.python.org/en/latest/specifications/dependency-specifiers/) and [npm semver](https://github.com/npm/node-semver#ranges): only the documented numeric subset is implemented; third-party implementations were not copied.
