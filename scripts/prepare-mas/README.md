# Prepare the pinned App Store manager

This build helper downloads the official mas **7.0.0** release for `arm64` or
`x86_64`, verifies the archive and extracted executable SHA-256 pins, and writes
a new private staging directory. It never installs the package, executes mas,
signs a binary, registers a helper, or changes host software.

From the repository root on macOS with Go available:

```sh
stage="$(mktemp -d /private/tmp/keepupdated-mas.XXXXXX)"
go run ./scripts/prepare-mas --arch arm64 --out "$stage/prepared"
```

`--out` must not exist. Its parent must be a directory owned by the current user
with no group/other access. Output contains:

- `mas`: the unmodified upstream **native executable**, not the zsh wrapper.
- `licenses/`: mas and the 11 locked Swift dependency license/notice records.
- `SOURCE.json`: pinned source, architecture, hashes, minimum OS, dylib imports,
  and upstream rpaths. The same metadata is printed as JSON on stdout.

The caller can copy `mas` to `Contents/Library/Tools/mas` and retain `licenses/`
and `SOURCE.json` in the application resources. Desktop-build integration and
signing are owned by the caller; this helper does not modify shared build/CI
scripts. Sign the copied nested executable under the product's intended
identifier and seal the enclosing bundle. A post-sign executable hash differs
from the upstream input hash; bind the actual packaged executable at runtime.

An ad-hoc candidate still cannot pass the shared same-Developer-ID execution
boundary. Developer ID signing, helper activation, user-session validation, and
an isolated real App Store transaction remain separate acceptance steps.

## Verified inputs and checks

[assets.json](assets.json) is the pin source. The official
[v7.0.0 release](https://github.com/mas-cli/mas/releases/tag/v7.0.0) points to
commit `7c70ffdfd9f71a654300a78b3b627782e6abe1b4`. Downloaded archive hashes match
the release asset API digests. Independently repeated extraction produces the
same executable hashes.

| Architecture | Archive SHA-256 | Native executable SHA-256 |
| --- | --- | --- |
| arm64 | `bc218a854c85d9e1c95496a96b26287bb66056b95688b90f91d04fa62ed21b75` | `1a9ec80879b029b67fe3de485203722cfdbee8589fdd70d3bf3c3df59c9f32c9` |
| x86_64 | `9bca1de1fb6ea19c9e0b9d7b7c85249020c9ed65a9d434c2ef40896a2f3395f9` | `7bdd3967f62f5439fdc4ad3dd3a1d0972cb43ba09fd736bc58720121467c07ee` |

Both packages declare version 7.0.0 and minimum macOS 13. Both thin Mach-O
executables declare minimum macOS 13.0, SDK 26.4, and contain the pinned source
revision. The helper checks package identity/version, executable hash/type/CPU,
embedded build identity, `LC_BUILD_VERSION`, and all dylib-import command forms,
including weak imports. All 27 imports in each official executable use
`/System/Library/` or `/usr/lib/`, matching the shared boundary's current policy.
This includes Apple's private CommerceKit and StoreFoundation frameworks; it is
not a claim that the implementation uses only public APIs or has been tested on
every supported OS.

The binaries also contain rpaths for `/usr/lib/swift`, `@loader_path`, and the
upstream Xcode toolchain. No imported dylib uses `@rpath`. These search paths are
recorded separately rather than hidden or rewritten. A future policy forbidding
non-system rpaths would require a separately reviewed artifact change.

The upstream packages are unsigned. The arm64 executable has an ad-hoc signature;
the Intel executable is unsigned. Pinning these inputs does not establish a
Developer ID identity. No downloaded manager was executed during preparation.

## Notices and source builds

The packaged mas MIT license matches the pinned source. Preserve the files under
[licenses](licenses), including the Apache NOTICE files.
[licenses/SOURCES.json](licenses/SOURCES.json) records exact source revisions,
URLs, and content hashes. The inventory covers the dependencies in mas's pinned
`Package.resolved`: MIT, Apache-2.0, and Apache-2.0 with the Swift runtime exception.
Hitch's MIT text is extracted from its pinned README's license section; its
source location is retained.

A source rebuild is unnecessary for this input-preparation path. The pinned
source and lock file are available, but the upstream prebuild embeds Git and
compiler metadata, and its release workflow selects Xcode 26.4. A bit-identical
source rebuild has not been established. Prefer the verified release bytes to
introducing an additional dependency/build pipeline here.
