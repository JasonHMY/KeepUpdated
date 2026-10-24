import Foundation

enum InventoryStatusText {
    struct Presentation {
        let title: String
        let explanation: String
        let symbol: String
    }

    static func describe(_ item: InventoryItem) -> Presentation {
        if item.kind == "mcp-server" {
            if item.evidence.contains("disabled") {
                return .init(title: L10n.text("Disabled"), explanation: L10n.text("This connection is disabled in its client. This scan did not start or contact it."), symbol: "pause.circle")
            }
            if item.source == "remote" {
                if item.evidence.contains("endpoint:loopback") {
                    return .init(title: L10n.text("Local connection"), explanation: L10n.text("This connection points to a service on this Mac. Update the program that provides it."), symbol: "desktopcomputer")
                }
                if item.evidence.contains("endpoint:remote") {
                    return .init(title: L10n.text("Network connection"), explanation: L10n.text("This Mac holds connection settings. The service is updated on the server, not through this connection."), symbol: "network")
                }
                return .init(title: L10n.text("Connection needs review"), explanation: L10n.text("The connection address could not be classified. Review it in the original client; no service was contacted."), symbol: "questionmark.circle")
            }
        }
        if item.kind == "python-package", !item.isDirectPythonTool {
            return .init(title: L10n.text("Dependency library"), explanation: L10n.text("This library belongs to a Python tool or environment. Update it through that owner."), symbol: "shippingbox")
        }
        if item.owner == "volta" {
            return .init(title: L10n.text("Managed by Volta"), explanation: ReasonText.describe("volta_owner_manual"), symbol: "shippingbox")
        }
        if let manager = item.skillManager {
            let title = ["skillshare_managed", "skillshare_local_changes", "skillshare_baseline_missing"].contains(manager.code)
                ? L10n.text("Managed by Skillshare") : L10n.text("Skillshare record needs review")
            return .init(title: title, explanation: ReasonText.describe(manager.code), symbol: "folder.badge.gearshape")
        }
        if item.isUnregisteredSkillCache {
            return .init(title: L10n.text("Unregistered cache"), explanation: L10n.text("This cache is not in the current plugin registry. It is kept on disk and excluded from installed Skill counts."), symbol: "archivebox")
        }
        if item.isUnverifiedSkillCache {
            return .init(title: L10n.text("Registration unverified"), explanation: L10n.text("Skill files were found in a plugin cache, but the current installation is not verified. Manage them in the owning client."), symbol: "questionmark.folder")
        }
        if item.source == "plugin-cache" && (item.plugin != nil || item.update.code.hasPrefix("plugin_")) {
            if item.update.status == "current" {
                return .init(title: L10n.text("Plugin snapshot matches"), explanation: L10n.text("The registered plugin matches the checked snapshot. This does not verify remote freshness or the version loaded by running sessions."), symbol: "checkmark.circle")
            }
            if item.canUpdate {
                return .init(title: L10n.text("Plugin update candidate"), explanation: L10n.text("Preview the whole-plugin scope and exact source snapshot before updating."), symbol: "arrow.up.circle.fill")
            }
            return .init(title: L10n.text("Managed by plugin"), explanation: ReasonText.describe(item.update.code), symbol: "puzzlepiece.extension")
        }
        if item.update.status == "current" {
            if item.update.code == "skill_content_current" {
                return .init(title: L10n.text("Matches source"), explanation: L10n.text("All local Skill content matches the recorded source. No update is needed."), symbol: "checkmark.circle")
            }
            if item.channel == "public-stable" {
                return .init(title: L10n.text("Matches stable release"), explanation: L10n.text("The version matches the vendor's public stable release. This does not mean in-app update settings were read."), symbol: "checkmark.circle")
            }
            return .init(title: L10n.text("Up to date"), explanation: L10n.text("The update source was checked. The current version needs no update."), symbol: "checkmark.circle")
        }
        if item.canUpdate {
            if item.appStoreURL != nil {
                return .init(title: L10n.text("Update available"), explanation: L10n.text("A newer Mac version is confirmed. Open its App Store page to update, then check again."), symbol: "arrow.up.circle.fill")
            }
            if item.needsManualUpdate && item.canOpenForManualUpdate {
                return .init(title: L10n.text("Update available"), explanation: L10n.text("Open App to update yourself"), symbol: "arrow.up.circle.fill")
            }
            if ["skill_update_available", "skill_update_available_manual"].contains(item.update.code) {
                return .init(title: L10n.text("Skill update available"), explanation: L10n.text("New upstream content is available and local content still matches the installation record. Choose Update to review file changes first."), symbol: "arrow.up.circle.fill")
            }
            if item.channel == "public-stable" {
                return .init(title: L10n.text("Stable update available"), explanation: L10n.text("The vendor's public stable release is newer. Choose Update Options to review how to update; confirm upgrade requirements in the app."), symbol: "arrow.up.circle.fill")
            }
            return .init(title: L10n.text("Update available"), explanation: L10n.text("A newer version is confirmed by the update source. Choose Update to preview the operation first."), symbol: "arrow.up.circle.fill")
        }
        if !["unknown", "unsupported", "error"].contains(item.update.status) {
            return .init(title: L10n.text("Unsupported status"), explanation: L10n.text("This version cannot interpret the check result or determine whether an update is needed. Open Source & Evidence for the check code."), symbol: "info.circle")
        }
        if item.update.code == "no_update_source", let issue = healthExplanation(item) {
            return .init(title: L10n.text("Installation issue"), explanation: issue, symbol: "exclamationmark.triangle")
        }
        let code = item.update.code
        if code.hasPrefix("skill_git_") {
            return .init(title: L10n.text("Fallback read incomplete"), explanation: ReasonText.describe(code), symbol: "exclamationmark.triangle")
        }
        if code == "public_registry_candidate_unverified_origin" {
            let explanation = item.latestVersion.map { L10n.text("The public registry has version %@, but its source cannot be matched to this installation. Check with the original installation tool.", $0) } ?? L10n.text("A package with the same name was found in the public registry, but its source cannot be matched to this installation. Check with the original installation tool.")
            return .init(title: L10n.text("Unknown install source"), explanation: explanation, symbol: "shippingbox")
        }
        if code == "no_update_source" {
            if item.owner == "app-component" {
                return .init(title: L10n.text("Updates with parent app"), explanation: L10n.text("This is an internal app component without its own updater. Update its parent app."), symbol: "square.stack")
            }
            if item.owner == "homebrew" {
                return .init(title: L10n.text("Managed by Homebrew"), explanation: L10n.text("This entry has no independent version source. Find its installation in the Homebrew category."), symbol: "shippingbox")
            }
            if item.kind == "application" {
                return .init(title: L10n.text("No update source found"), explanation: L10n.text("No readable update address or matching version lookup was found. Check for updates in the app."), symbol: "link")
            }
            return .init(title: L10n.text("Version source missing"), explanation: L10n.text("No verifiable version source is available. Check for updates with the original installation tool."), symbol: "info.circle")
        }
        if code == "use_installation_owner" {
            return .init(title: L10n.text("Managed by parent package"), explanation: L10n.text("This is an entry point or configuration for an existing installation. Check and update its package entry."), symbol: "shippingbox")
        }
        if let text = reasons[code] {
            let symbol = item.update.status == "error" ? "exclamationmark.triangle" : (code == "online_query_required" || code == "mac_store_not_queried" ? "magnifyingglass" : "info.circle")
            return .init(title: L10n.text(text.0), explanation: L10n.text(text.1), symbol: symbol)
        }
        if item.update.status == "error" {
            return .init(title: L10n.text("Version check failed"), explanation: L10n.text("No valid version result was received. Try Check for Updates again; the failure code is in Source & Evidence."), symbol: "exclamationmark.triangle")
        }
        return .init(title: L10n.text("Check not supported"), explanation: L10n.text("KeepUpdated cannot interpret this result or determine whether an update is needed. Check with the original installation tool or open Source & Evidence for the check code."), symbol: "info.circle")
    }

    static func healthExplanation(_ item: InventoryItem) -> String? {
        guard item.health.status == "error" else { return nil }
        switch item.health.code {
        case "bundle_metadata_unreadable": return L10n.text("The app metadata could not be read, so its local version could not be verified. Check the app files and read permissions.")
        case "config_unreadable", "service_definition_unreadable": return L10n.text("The configuration could not be read. Check that the file still exists and the current user can read it.")
        case "config_parse_failed", "invalid_mcp_server_map", "invalid_server_entry": return L10n.text("The configuration uses an unsupported format. Check it in the original client; the check code is in Source & Evidence.")
        case "command_missing_or_inaccessible", "missing_command", "command_not_executable": return L10n.text("The configured program is missing, inaccessible or not executable. Check its command path in the original client.")
        case "package_link_or_metadata_missing", "broken_or_inaccessible_link": return L10n.text("The installation link is broken or its metadata is missing. Repair it with the original installation tool.")
        case "package_metadata_unreadable", "metadata_unreadable", "metadata_header_too_large", "skill_entry_unreadable", "entry_unreadable": return L10n.text("Installation metadata could not be read completely. Check the files and read permissions.")
        case "active_receipt_unreadable": return L10n.text("The active Homebrew version's installation record could not be read. Check the record and read permissions.")
        case "not_executable": return L10n.text("The installation entry is not executable. Check its file type and permissions.")
        case "service_label_missing": return L10n.text("The launch configuration has no service identifier, so its running state cannot be checked. Repair the configuration through its app.")
        case "interpreter_missing_or_broken", "interpreter_not_executable": return L10n.text("The Python environment's interpreter is missing or not executable. Repair it with the tool that owns the environment.")
        default:
            let issues = item.dependencies?.filter { $0.status == "error" }.map(\.name) ?? []
            if !issues.isEmpty { return L10n.text("Dependency issues: %@. Open Source & Evidence for the requirements.", issues.prefix(3).joined(separator: L10n.listSeparator)) }
            return L10n.text("The local installation check found issues. Open Source & Evidence for the check code and evidence.")
        }
    }

    private static let reasons: [String: (String, String)] = [
        "skill_referenced_by_preserved_installation": ("Skill still in use", "This Skill is still used by an installation being kept. Keep the shared content, or select those references and preview again."),
        "store_product_id_unverified": ("App Store identity unverified", "The store did not provide a valid product ID. No product page or Mac version can be safely selected."),
        "mac_store_product_unverified": ("Mac product unverified", "The Mac catalog did not confirm the same product, bundle and region. Update status remains unknown."),
        "mac_store_version_unverified": ("Mac version unverified", "The store did not provide a Mac software version. The phone version is not used."),
        "ambiguous_mac_store_versions": ("Mac version unverified", "The store returned conflicting Mac versions. Check in the App Store."),
        "invalid_store_country": ("App Store region unverified", "The store region is invalid. Check the App Store region and retry."),
        "github_rate_limited": ("GitHub rate limit", "The GitHub query limit has been reached. Try again later."),
        "vendor_product_unresolved": ("Product version unverified", "Product, version or build metadata is inconsistent, so a reliable update source cannot be selected. Check for updates in the app."),
        "electron_configuration_unresolved": ("Update configuration unknown", "The app's update configuration contains unsupported options. Check for updates in the app."),
        "electron_source_unverified": ("Update source unverified", "The bundled update address differs from the verified official source. It was not used."),
        "electron_channel_unresolved": ("Update channel unverified", "The configured update channel is not supported yet. Check for updates in the app."),
        "electron_architecture_unresolved": ("No matching package", "The update source does not identify a macOS build for this processor. Update eligibility is unknown."),
        "electron_metadata_unresolved": ("Update manifest unverified", "The vendor's update manifest is missing required fields or has changed format. Retry later or use the in-app updater."),
        "electron_constraints_unresolved": ("Upgrade requirements unverified", "The manifest includes a phased rollout or system restrictions that cannot be fully evaluated. Check for updates in the app."),
        "skill_local_modified": ("Local content differs", "Local Skill content differs from the installation record. The source has no new changes; local content will be preserved."),
        "skill_upstream_changed_local_modified": ("Local and upstream changes", "Both upstream and local Skill content have changed. Choose Review Changes to compare them; local content will be preserved."),
        "skill_local_modified_or_baseline_unknown": ("Install baseline unverified", "The local Skill's installation baseline could not be verified. Choose Review Changes to compare; local content will be preserved."),
        "skill_local_hash_unavailable": ("Local content unreadable", "Skill files could not be fully checked, so update status is unknown. Check file read permissions."),
        "skill_source_conflict": ("Conflicting source records", "This Skill has conflicting source records. Review its original installation records first."),
        "skill_upstream_path_missing": ("Source path missing", "The source repository was found, but the recorded Skill path was not. Check whether it was moved or removed."),
        "skill_tree_incomplete": ("Source content incomplete", "The source returned an incomplete file list. It cannot establish that the installation is current. Retry later."),
        "online_query_required": ("Not checked for updates", "Only local installation data has been read. Choose Check for Updates at the top to look for new versions."),
        "mac_store_not_queried": ("App Store not checked", "The store version has not been queried. Choose Check for Updates, or check in the App Store."),
        "skill_upstream_unknown": ("Skill source not recorded", "A local SKILL.md was found, but no verifiable upstream repository or installation record. Update through the original installation method."),
        "git_remote_not_fetched": ("Remote commits not compared", "Only local Git state has been read; the remote version has not been checked. Check for updates in the original repository."),
        "managed_by_plugin_owner": ("Managed by plugin", "This Skill is provided or cached by a plugin. Update the owning plugin instead of replacing this file separately."),
        "configuration_not_update_target": ("Configuration needs no update", "This MCP configuration has no independent software version. Manage its servers or their packages."),
        "server_source_not_verified": ("MCP ownership unknown", "The server entry has not been matched to a verifiable package. Check its installation method in the original agent's MCP settings."),
        "remote_not_locally_managed": ("Remote service", "The server runs remotely; this Mac only holds connection settings. Its provider maintains the version."),
        "environment_not_update_target": ("Python environment", "The environment is not an independent update target. Review the tools or packages inside it."),
        "service_not_update_target": ("Background service configuration", "This service launch configuration has no independent update version. Update the app or package providing it."),
        "custom_tap_update_unimplemented": ("Custom source unsupported", "This software comes from a custom Homebrew tap whose remote version cannot be verified. Check with the original tap."),
        "tap_remote_not_fetched": ("Tap repository not checked", "The Homebrew tap was discovered but its repository updates were not compared. Check through Homebrew."),
        "direct_install_not_registry": ("Direct installation", "This version was installed from a local file, link or repository. The public registry is not its update source; check the original source."),
        "cask_source_unresolved": ("Incomplete install source", "The cask record does not establish its source. Check the original Homebrew installation record."),
        "system_update_query_not_requested": ("System updates not checked", "Regular checks do not query macOS updates. Check System Settings → General → Software Update."),
        "timeout": ("Version check timed out", "The version server did not respond in time. Try Check for Updates again later."),
        "query_failed": ("Version check failed", "No valid response was received from the version server. Check the network and retry, or use the original installation tool."),
        "http_error": ("Update server error", "The update server returned an error. Retry later; if it persists, check with the original installation tool."),
        "not_found": ("Update record missing", "The server returned no matching software record. Check the product name and installation method with the original source."),
        "invalid_response": ("Invalid version data", "The version server's response could not be parsed. Retry later or check with the original installation tool."),
        "output_limit": ("Version data too large", "The update data exceeds the supported size, so this check stopped. Check with the original installation tool."),
        "no_matching_mac_product": ("No matching Mac product", "The store query did not match a Mac product for this app, possibly due to region or product records. Check in the App Store."),
        "ambiguous_store_results": ("Conflicting store records", "The store returned conflicting versions. Update availability is unknown; check in the App Store."),
        "identity_mismatch": ("Product identity mismatch", "The update record does not match the local product identity. A shared name is not enough; check the original installation source."),
        "catalog_definition_mismatch": ("Installation definition mismatch", "The version catalog and local installation definition differ. Check with the original package manager."),
        "platform_variation_unresolved": ("Applicable system version unknown", "The update source lists versions for different systems. A version for this Mac could not be selected."),
        "version_scheme_or_active_version_unresolved": ("Versions cannot be compared", "The version format or active version cannot be compared reliably. Check with the original app or installation tool."),
        "installed_newer_than_source": ("Installed version is newer", "The installed version is newer than the query result and may use a different channel. Check the update channel before replacing it."),
        "missing_latest_version": ("No source version returned", "The update source was queried but returned no valid version. Retry later or check with the original installation tool."),
        "active_keg_unresolved": ("Active version unknown", "The active Homebrew version link could not be verified. Check the installation links through Homebrew first."),
        "multiple_cask_versions": ("Multiple installed versions", "Multiple cask versions were found, but the active version is unknown. Check the installation location first."),
        "invalid_package_identity": ("Invalid package identity", "The package name in the metadata cannot be queried reliably. Check the metadata with the original installation tool."),
        "vendor_adapter_missing": ("Vendor updater unsupported", "No version lookup is available for this app yet. Check for updates in the app."),
        "vendor_channel_unresolved": ("Update channel unknown", "The app's stable or alternative update channel has not been identified. Check its in-app update settings."),
        "vendor_architecture_unresolved": ("Processor version unknown", "A matching processor build could not be identified for this installation. Check through the in-app updater."),
        "vendor_minimum_version_unresolved": ("Required prior version unknown", "The vendor's minimum upgrade version requirement could not be verified. Check through the in-app updater."),
        "application_payload_activity_unknown": ("Running version unknown", "The app includes independently updated content, so the bundle version may differ from the running version. Check in the app."),
        "feed_requires_unsafe_or_authenticated_request": ("Update address needs review", "The update address requires authentication or does not meet request requirements. It was not accessed; use the in-app updater."),
        "invalid_source": ("Invalid update address", "The update address in the installation record cannot be used. Check for updates in the app."),
        "invalid_appcast": ("Invalid update manifest", "The app's update manifest could not be parsed. Retry later or use the in-app updater."),
        "appcast_conflicting_metadata": ("Conflicting update manifest", "The update manifest contains conflicting version records. A reliable update version could not be selected."),
        "no_compatible_default_channel_release": ("No compatible release", "The current channel has no release matching this system or processor. Check in the app."),
        "feed_constraints_unresolved": ("Upgrade requirements unknown", "The update manifest includes unrecognized system or version restrictions. Check through the in-app updater."),
        "upstream_definition_absent": ("Upstream definition missing", "The item is absent from both the upstream version catalog and definitions. Check the original source; this does not mean it should be uninstalled."),
        "upstream_alias_requires_review": ("Upstream alias needs review", "The upstream name now points to another item. Verify that it is the same software; it will not be replaced automatically."),
        "upstream_rename_requires_review": ("Upstream name changed", "Upstream records a name change. Check the migration relationship with the original installation tool."),
        "unrecognized_upstream_alias": ("Unknown upstream alias", "The upstream name changed, but the mapping could not be verified. Check the original source."),
        "system_query_failed": ("System update check failed", "The macOS update check did not complete. Retry in System Settings → Software Update."),
        "unrecognized_system_response": ("Invalid system response", "The system returned unrecognized update information. Check in System Settings.")
    ]
}
