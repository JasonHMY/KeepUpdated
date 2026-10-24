// Package manage plans and executes explicitly confirmed installation changes.
package manage

import (
	"context"
	"net/http"
	"time"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/configedit"
	"github.com/JasonHMY/KeepUpdated/internal/mcpref"
	"github.com/JasonHMY/KeepUpdated/internal/privileged"
)

const PlanSchema = "keepupdated/plan/v1"
const ApplySchema = "keepupdated/apply/v1"

type Selection struct {
	MCPCandidate      string              `json:"mcp_candidate,omitempty"`
	CachedPlugins     bool                `json:"cached_plugins,omitempty"`
	Action            string              `json:"action"`
	IDs               []string            `json:"ids,omitempty"`
	All               bool                `json:"all"`
	Cleanup           bool                `json:"cleanup"`
	Special           bool                `json:"special"`
	QuitApps          []string            `json:"quit_apps,omitempty"`
	ReopenApps        []string            `json:"reopen_apps,omitempty"`
	MCPReference      string              `json:"mcp_reference,omitempty"`
	Consents          []string            `json:"consents,omitempty"`
	PrivilegedRequest *privileged.Request `json:"privileged_request,omitempty"`
}

// Options are supplied by the application, never deserialized from a plan.
type Options struct {
	recordRecovery        func(backup, recovery string) error
	Context               context.Context
	Progress              func(audit.Progress)
	Home                  string
	BrewRoots             []string
	ManagerPaths          map[string]string
	Now                   time.Time
	TTL                   time.Duration
	ResolvedTargets       map[string]ResolvedTarget
	SkillRoots            []string
	AppRoots              []string
	SkillHTTPClient       *http.Client
	ApplicationHTTPClient *http.Client
	ManagerRoots          map[string]string
	ManagerBins           map[string]string
	NativeCLIHTTPClient   *http.Client
	PrivilegedClient      privileged.Client
}

// ResolvedTarget is trusted output from an owner-native, read-only resolver.
// Source is a public label or digest, never a credential-bearing registry URL.
type ResolvedTarget struct {
	Python               *PythonPolicy               `json:"python_policy,omitempty"`
	MCPDiscovery         *mcpref.Discovery           `json:"mcp_discovery,omitempty"`
	Node                 *NodePolicy                 `json:"node_policy,omitempty"`
	PluginUpdate         *PluginUpdate               `json:"plugin_update,omitempty"`
	Version              string                      `json:"version"`
	FromVersion          string                      `json:"from_version,omitempty"`
	Source               string                      `json:"source"`
	Provider             string                      `json:"provider"`
	ManagerPath          string                      `json:"manager_path"`
	Scope                string                      `json:"scope"`
	OriginVerified       bool                        `json:"origin_verified"`
	HasLifecycleScripts  bool                        `json:"has_lifecycle_scripts"`
	Reason               string                      `json:"reason,omitempty"`
	AffectedPackages     []string                    `json:"affected_packages,omitempty"`
	SkillUpdate          *CopiedSkillPreview         `json:"skill_update,omitempty"`
	ApplicationLifecycle *ApplicationLifecycleTarget `json:"application_lifecycle,omitempty"`
	ApplicationState     string                      `json:"application_state,omitempty"`
	ApplicationUpdate    *ApplicationUpdatePreview   `json:"application_update,omitempty"`
	ResolutionError      string                      `json:"resolution_error,omitempty"`
	RuntimeDependencies  []RuntimeDependency         `json:"runtime_dependencies,omitempty"`
	RuntimeDependents    []string                    `json:"runtime_dependents,omitempty"`
	RuntimeScopeVerified bool                        `json:"runtime_scope_verified,omitempty"`
	NativeCLI            *NativeCLIUpdate            `json:"native_cli,omitempty"`
	PrivilegedRequest    *privileged.Request         `json:"privileged_request,omitempty"`
	AppStore             *AppStoreBinding            `json:"app_store,omitempty"`
}

type Binding struct {
	Python                       *PythonPolicy   `json:"python_policy,omitempty"`
	PythonEntryFingerprint       string          `json:"python_entry_fingerprint,omitempty"`
	NodeVirtualStore             string          `json:"node_virtual_store,omitempty"`
	ManagerEnvFingerprint        string          `json:"manager_environment_fingerprint,omitempty"`
	TargetID                     string          `json:"target_id,omitempty"`
	PackageName                  string          `json:"package_name,omitempty"`
	InstallationKind             string          `json:"installation_kind,omitempty"`
	Path                         string          `json:"path"`
	RealPath                     string          `json:"real_path"`
	Owner                        string          `json:"owner"`
	Identity                     string          `json:"identity"`
	Source                       string          `json:"source"`
	Channel                      string          `json:"channel"`
	Fingerprint                  string          `json:"fingerprint"`
	ManagerPath                  string          `json:"manager_path,omitempty"`
	ManagerFingerprint           string          `json:"manager_fingerprint,omitempty"`
	Scope                        string          `json:"scope,omitempty"`
	ResolvedTarget               *ResolvedTarget `json:"resolved_target,omitempty"`
	WasActive                    bool            `json:"was_active"`
	PythonRequirementFingerprint string          `json:"python_requirement_fingerprint,omitempty"`
}

type PlanItem struct {
	MCPDiscovery         *mcpref.Discovery            `json:"mcp_discovery,omitempty"`
	CanonicalID          string                       `json:"canonical_id,omitempty"`
	PluginUpdate         *PluginUpdate                `json:"plugin_update,omitempty"`
	ID                   string                       `json:"id"`
	Name                 string                       `json:"name"`
	Kind                 string                       `json:"kind"`
	Provider             string                       `json:"provider"`
	FromVersion          string                       `json:"from_version"`
	TargetVersion        string                       `json:"target_version,omitempty"`
	Status               string                       `json:"status"`
	Reason               string                       `json:"reason"`
	RequiresAdmin        bool                         `json:"requires_admin"`
	RequiresQuit         bool                         `json:"requires_quit"`
	RequiresRestart      bool                         `json:"requires_restart"`
	RequiresScripts      bool                         `json:"requires_scripts"`
	SpecialApproved      bool                         `json:"special_approved"`
	Cleanup              bool                         `json:"cleanup"`
	AffectedPaths        []string                     `json:"affected_paths"`
	AffectedIDs          []string                     `json:"affected_ids"`
	CleanupPaths         []string                     `json:"cleanup_paths"`
	Applications         []ApplicationBinding         `json:"applications,omitempty"`
	ApplicationLifecycle []ApplicationLifecycleTarget `json:"application_lifecycle,omitempty"`
	MCP                  *configedit.Target           `json:"mcp,omitempty"`
	Binding              Binding                      `json:"binding"`
	SkillUpdate          *CopiedSkillPreview          `json:"skill_update,omitempty"`
	RequiredConsents     []string                     `json:"required_consents,omitempty"`
	RuntimeReview        *RuntimeReview               `json:"runtime_review,omitempty"`
	PrivilegedRequest    *privileged.Request          `json:"privileged_request,omitempty"`
}

type ApplicationBinding struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Identity string `json:"identity"`
	Version  string `json:"version"`
}

type Plan struct {
	Schema    string     `json:"schema"`
	PlanID    string     `json:"plan_id"`
	CreatedAt string     `json:"created_at"`
	ExpiresAt string     `json:"expires_at"`
	Action    string     `json:"action"`
	Selection Selection  `json:"selection"`
	Items     []PlanItem `json:"items"`
}

type ItemResult struct {
	RecoveryPaths         []string                     `json:"recovery_paths,omitempty"`
	CanonicalID           string                       `json:"canonical_id,omitempty"`
	MarketplaceRefresh    *MarketplaceRefreshResult    `json:"marketplace_refresh,omitempty"`
	ReloadRequired        bool                         `json:"reload_required,omitempty"`
	ID                    string                       `json:"id"`
	Name                  string                       `json:"name"`
	Status                string                       `json:"status"`
	Reason                string                       `json:"reason"`
	BeforeVersion         string                       `json:"before_version,omitempty"`
	AfterVersion          string                       `json:"after_version,omitempty"`
	Changed               bool                         `json:"changed"`
	Executed              bool                         `json:"executed"`
	CommandSucceeded      bool                         `json:"command_succeeded"`
	RelatedChanges        []Change                     `json:"related_changes,omitempty"`
	BackupPath            string                       `json:"backup_path,omitempty"`
	ApplicationLifecycle  []ApplicationLifecycleResult `json:"application_lifecycle,omitempty"`
	PrivilegedOperationID string                       `json:"privileged_operation_id,omitempty"`
	RecoveryPath          string                       `json:"recovery_path,omitempty"`
}

type Change struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BeforeVersion string `json:"before_version,omitempty"`
	AfterVersion  string `json:"after_version,omitempty"`
	Status        string `json:"status"`
}

type ApplyResult struct {
	Action      string       `json:"action,omitempty"`
	State       string       `json:"state,omitempty"`
	OriginalIDs []string     `json:"original_ids,omitempty"`
	Schema      string       `json:"schema"`
	PlanID      string       `json:"plan_id"`
	StartedAt   string       `json:"started_at"`
	FinishedAt  string       `json:"finished_at"`
	Results     []ItemResult `json:"results"`
}

type Command struct {
	Path string
	Args []string
	Env  []string
	Dir  string
}

type Runner interface {
	Run(context.Context, Command) ([]byte, error)
}

type Engine struct {
	RecordOperation  func(ApplyResult) error
	Snapshot         func(context.Context) (audit.Report, error)
	Runner           Runner
	Options          Options
	LifecycleTimeout time.Duration
	StateDir         string
	Now              func() time.Time
	ResolveTargets   func(context.Context, audit.Report, Selection) (map[string]ResolvedTarget, error)
}
