# KeepUpdated local plugin

Requires an Apple Silicon Mac running macOS 13 or later and a desktop client with local stdio MCP support. The package includes the complete KeepUpdatedAgent App and matching Skill. No Go, Node or Python runtime is required.

Use the eight MCP tools to inspect capabilities, scan inventory, check updates, page or inspect saved items, preview changes, request execution and read an operation's results. Checking updates can query public sources when `online` is true. Scans and previews save private records and may evict older cache entries. Previewing never installs software.

Before execution, review the exact installation IDs, changes and effects. `request_execution` opens the existing native confirmation window. Only that confirmation can approve the saved plan; installing this plugin or giving chat consent does not approve host changes. Unsupported and privileged actions remain blocked or manual. After an interruption, query the same operation ID and inspect every item. Never automatically retry an unknown operation.

The top-level Skill uses the connected MCP workflow when available, including native approval and per-operation recovery. Its CLI instructions remain available for an explicitly requested local CLI workflow; they are not a fallback around refused native approval. Its launcher delegates to the sealed App's matching Skill launcher, which validates the CLI version, platform and schemas. Keep the entire package together; do not move the CLI out of the App or edit the App's resources.

This is an ad-hoc local candidate, not a public release or notarized distribution. Local installation and stdio verification do not establish desktop chat, native approval UI, account eligibility or customer-device acceptance.
