# Local MCP update ownership

KeepUpdated distinguishes an installed MCP server from the launcher that starts it.
A remote MCP service remains outside local software management. Scanning reads
configuration and installation metadata without starting servers.

Stable Node/Homebrew entries keep their existing owner mapping. A pipx or uv tool
entry can now map to its installed Python package when the console script, tool
receipt, stable executable path and SHA-256 RECORD contents agree. The update then
uses the existing manager provider. Pinned requirements, unsupported source
settings, ambiguous owners and modified recorded files remain manual.

Owner mapping accepts absolute executable paths and bare commands with a known
PATH lookup. Relative command paths, earlier relative or empty PATH entries,
ambiguous argument shapes, and bare commands with a configured `env.PATH` or
`environment.PATH` remain unknown. Unparsed TOML/YAML environments also keep bare
commands manual; they do not change an absolute command's owner mapping.
Preview rechecks the scanned configuration, executable entry and unique package
owner. Changes to those bindings require a fresh preview. The scan's PATH stays
private, so a decoded report cannot stand in for fresh PATH lookup evidence.
Direct npm launcher scripts (`npx-cli.js` and `npm-cli.js`) also remain manual;
their owning npm installation is not the server named in their arguments.

Python ownership currently covers the standard user tool directories and
`~/.local/bin` exports, plus direct `bin` entries in those tool environments.
It also accepts clean absolute paths supplied through `PIPX_HOME`/`PIPX_BIN_DIR`
and `UV_TOOL_DIR`/`UV_TOOL_BIN_DIR`; pipx environments are under
`PIPX_HOME/venvs`. Supply the same overrides to scan, plan and apply. Unconfigured
roots or bin paths, injected dependencies, unsupported receipt layouts and files
without verifiable SHA-256 RECORD entries remain manual.

## Change one launcher package reference

An `npx`, `bunx`, `uvx` or `uv tool run` definition can carry a package reference without a
persistent installation. Updating the launcher does not establish that this server
was updated. uv also distinguishes cached tool execution from installed tool
environments; see the [uv tool documentation](https://docs.astral.sh/uv/concepts/tools/).
The [npx documentation](https://docs.npmjs.com/cli/v11/commands/npx/) describes
package inference and its cache behavior.

Use a separate configuration migration when you want to change the package
reference. For a synthetic `example-mcp@1.0.0` definition:

```sh
keepupdated plan --action update --id MCP_ITEM_ID --special \
  --mcp-reference example-mcp@2.0.0 > reference-plan.json
```

Review the `mcp.package_reference.before` and `after` values, the selected config
path and the exact plan ID. Then apply that saved preview:

```sh
keepupdated apply --plan reference-plan.json --confirm EXACT_PLAN_ID
```

The migration requires one explicitly selected server, `--special`, and an exact
replacement version of the same package. Without `--mcp-candidate`, it does not run a launcher, query the
registry, download the replacement, or verify that the requested release exists.
When the client next starts the server, its existing source settings and normal
launcher behavior still apply. A client that watches its config may reload it.

Supported forms are deliberately narrow:

- `npx [-y|--yes] package[@version]`
- `npx [--yes] --package package@version executable` (also `-p` and `--package=`)
- `bunx package[@version] [serverArgs…]` (scoped or unscoped npm package)
- `uvx package`, `uvx package==version`, or `uvx package@version`
- `uvx --from package==version executable` (also `--from=`)
- The same uv forms after `uv tool run`

An unversioned or `latest` reference can be replaced with an exact version.
Interpreter-looking uv/uvx requests, such as `python@3.12`, `pypy39` or
`--from cpython@3.12`, remain manual because they select a runtime. This restriction
also applies to replacement validation; it does not restrict same-named npm packages.
Version ranges, URLs, local paths, extra dependencies, source overrides in
launcher flags, duplicate launcher options and ambiguous command forms remain
manual. Arguments after the selected executable/package remain server arguments.

The bunx grammar follows [Bun's documented argument boundary](https://bun.sh/docs/pm/bunx#arguments-and-flags),
checked against [Bun v1.4.3's parser](https://github.com/oven-sh/bun/blob/c6da4a4d3010e5553438c60f6bd76d981976867c/src/runtime/cli/bunx_command.rs#L82-L160).
Only bare `bunx` or a clean absolute command path with basename `bunx` is accepted.
The path selects a static grammar; it does not authenticate the binary or establish
an installation owner. Relative paths, shell/env wrappers and `bun x` remain manual.
All flags before the package remain manual, including `--bun`, `--package`/`-p`,
registry, runtime and cache options. After the package, flags such as `--bun`,
`--package`/`-p` and `--install`, and repeated package strings, are server arguments
and remain unchanged. Unscoped `tsc` and `claude`, including versioned forms, remain
manual because Bun maps them to other packages. Bare `add` and `exec` also remain
manual because Bun can dispatch them as commands before package parsing.

JSON/JSONC edits replace one string token and preserve all other bytes, including
comments, environment values and arguments. OpenCode command arrays are supported.
Simple TOML accepts a direct server table and single-line quoted strings/argument
arrays; multiline strings, multiline arrays, inline server tables and ambiguous
keys remain manual. YAML migration is not supported.

The operation is `replace-package-reference`; existing definition removal remains
a separate operation. Apply rechecks the exact file, writes a private backup,
replaces the file atomically and verifies the result. An intervening edit blocks
the plan. Success is reported as `configured` with
`package_reference_changed_server_not_started`, while the MCP definition remains
in inventory. It is not an installed-version success.

## Select a registry candidate

An update preview for one explicitly selected MCP definition can now offer an
exact npm registry candidate. Update All does not query or select launcher
candidates. The preview keeps the configured reference/version separate from the
registry candidate; neither establishes an installed or running server version.
A failed or invalid registry response is `unknown`, never “up to date.” A matching
release means only that the configured reference matches registry metadata.

The first source adapter is deliberately limited to local Codex stdio definitions
in `.codex/config.toml`, using npm 7–11's npx launcher. It requires:

- A clean absolute `command` path resolving to an npm distribution's
  `bin/npx-cli.js`, with readable launcher and npm version metadata.
- An explicit absolute `cwd` that exists. The config's directory is never used as
  the client's working directory. Symlinked working directories use their real
  ancestor chain for npm configuration checks.
- An explicit `[mcp_servers.NAME.env]` table with absolute `HOME`,
  `NPM_CONFIG_USERCONFIG` and `NPM_CONFIG_GLOBALCONFIG` paths. An optional `PATH`
  must contain only absolute directories. Other environment entries, nonempty
  `env_vars`, inline environment tables and ambiguous TOML stay manual.
- Readable npm built-in, user, global and ancestor project `.npmrc` files, or
  verified absence. Files may contain only comments, blank lines or
  `registry=https://registry.npmjs.org/`. Scoped registry settings, credentials,
  other npm settings and unreadable files require manual review. A static absolute
  `prefix` in npm’s built-in config is required to locate the global lookup tree;
  both user/global config paths remain explicitly bound by the definition.
- No local/ancestor package manifests, lockfiles or `node_modules` trees, and an
  absent or empty `HOME/.npm/_npx` execution cache. These locations can satisfy an
  exact request without using the registry, so existing local/workspace/cache
  installations remain manual.
- A bounded, readable global package tree with no manifest naming the requested
  package, including aliases, and no matching literal global command. Global
  manifests and directory identities are bound; new or replaced shadows invalidate
  the selection. Unrelated global packages can remain installed.

These requirements bound the source for the selected static local definition.
They do not identify an active client session, certify the launcher binary or
resolve merged configuration from other client scopes or remote execution.
Do not add or change client configuration merely to make a preview eligible.
Other clients, bare/relative npx commands, bunx and Python launchers retain the
existing manual exact-reference migration. Missing candidate fields in older
records never imply verified source evidence.

This adapter follows npm's [configuration precedence](https://docs.npmjs.com/cli/v11/configuring-npm/npmrc/)
and Codex's [local stdio environment construction](https://github.com/openai/codex/blob/main/codex-rs/rmcp-client/src/utils.rs)
and [explicit working directory handling](https://github.com/openai/codex/blob/main/codex-rs/rmcp-client/src/stdio_server_launcher.rs).
The lookup guard follows [npm exec's local, global and execution-cache resolution](https://github.com/npm/cli/blob/v11.19.1/workspaces/libnpmexec/lib/index.js).
Queries read release metadata from the default registry over HTTPS, without
credentials, redirects, downloads, package-manager commands or server startup.
Candidates include ecosystem, package, exact version, source identity, original
config hash, metadata hash and the `exact_same_package` constraint result.
That result validates the reference grammar and package identity, not runtime,
engine or dependency compatibility. Deprecated or invalid releases stay manual.

In the desktop preview, choose **Select This Registry Candidate**, then approve
the exact configuration change and possible client reload. **Create Separate
Preview** still prepares one item; apply needs the new preview's confirmation.
Editing the replacement clears candidate selection and approval, and explicitly
returns to the unverified manual-reference flow.

The CLI exposes the same selection. First preview the selected ID, then use its
`mcp_discovery.candidate.reference` and `mcp_discovery.candidate_id`:

```sh
keepupdated plan --action update --id MCP_ITEM_ID
keepupdated plan --action update --id MCP_ITEM_ID --special \
  --mcp-reference example-mcp@2.0.0 --mcp-candidate CANDIDATE_ID > reference-plan.json
keepupdated apply --plan reference-plan.json --confirm EXACT_PLAN_ID
```

The candidate ID excludes observation time. Each preview records its observation
separately. Revalidation queries the chosen **exact** version, so a later `latest`
release never silently changes the approved target. A changed source, original
config, candidate metadata or missing release blocks the old selection. Apply
rechecks source and lookup state again after backup/staging, immediately before
the existing byte-preserving replacement. Source drift leaves the original config
untouched and retains the backup.
Success remains `configured`; no cache is cleared and no pin is removed implicitly.

## Validation boundary

Controlled tests cover owner routing, pins, source/configuration drift, duplicate owners and launcher options, exact bytes, backups, replay and per-item verification. Isolated Python ownership and token-edit samples do not establish public-registry server installation or real-client reload. Configuration success is `configured`; it never means a server was downloaded, started or updated.
