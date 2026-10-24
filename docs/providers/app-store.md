# App Store manual updates

App Store applications update through App Store. KeepUpdated shows a link and waits for the user to click it; selecting Update All, generating a preview or applying a saved plan never opens the store automatically.

## Discovery and navigation

`scan` retains receipt-based App Store discovery. Explicit online `check` retains its read-only Mac product/version lookup. Universal products need matching platform-specific Mac metadata; an iOS version is not accepted as a Mac update.

The optional report field `app_store` contains `id`, `bundle_id` and `country` for navigation. A positive ID, matching bundle identity and two-letter country produce the fixed `macappstore://apps.apple.com/<country>/app/id<ID>` URL. Otherwise the desktop uses `macappstore://showUpdatesPage`. Neither path guesses from the app name or performs an additional lookup to obtain a link.

Update All shows manual entries immediately while preparing other items. A selection containing only manual apps skips plan generation. Opening a link does not install software or mark the app updated; check again after completing the update in App Store.

## Plan and apply policy

Every App Store update item returns `manual` with reason `app_store_manual_update`. This decision precedes candidate parsing and typed privileged requests. It does not invoke `mas`, Spotlight identity probes, signature inspection, downloads or the authorization bridge. Ordinary inventory metadata discovery still runs for a CLI plan.

Apply rebuilds saved plans against the current policy, so an older ready plan cannot enable store installation. The retired store executor also returns manual handling if called directly. Explicit `--special` or administrator consent does not override this policy. Uninstall behavior remains separate.

The existing packaged manager inputs and native authorization types are retained for compatibility; their presence is not a supported App Store installation path.

## Verification

Controlled tests cover store-only and mixed selections, absent product identity, no manager calls, typed-request bypass attempts and older saved plans. Desktop tests cover fixed links, explicit clicks, manual-only short circuit and preservation of update status. These checks perform no store transaction or account change.
