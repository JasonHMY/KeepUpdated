// Package privileged is the sole typed transport to the native macOS authorization boundary.
package privileged

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

type PackageTarget struct {
	InfoSHA256          string `json:"infoSHA256"`
	CodeResourcesSHA256 string `json:"codeResourcesSHA256"`
	InstallationPath    string `json:"installationPath"`
	BundleID            string `json:"bundleID"`
	Path                string `json:"path"`
	SHA256              string `json:"sha256"`
	TeamID              string `json:"teamID"`
	ReceiptID           string `json:"receiptID"`
	FromVersion         string `json:"fromVersion"`
	Version             string `json:"version"`
	Source              string `json:"source"`
}
type SystemTarget struct {
	Label       string `json:"label"`
	Version     string `json:"version"`
	Build       string `json:"build"`
	FromVersion string `json:"fromVersion"`
	FromBuild   string `json:"fromBuild"`
}
type AppStoreTarget struct {
	ADAMID              string `json:"adamID"`
	BundleID            string `json:"bundleID"`
	Path                string `json:"path"`
	ReceiptSHA256       string `json:"receiptSHA256"`
	InfoSHA256          string `json:"infoSHA256"`
	CodeResourcesSHA256 string `json:"codeResourcesSHA256"`
	FromVersion         string `json:"fromVersion"`
	Version             string `json:"version"`
	Region              string `json:"region"`
	ManagerSHA256       string `json:"managerSHA256"`
	ManagerVersion      string `json:"managerVersion"`
}

// No executable, argv, environment, password or shell fields are accepted.
// App Store execution uses only a sealed bundled mas, never an arbitrary manager path.
type Request struct {
	OperationID string          `json:"operationID,omitempty"`
	Kind        string          `json:"kind"`
	Package     *PackageTarget  `json:"package,omitempty"`
	System      *SystemTarget   `json:"system,omitempty"`
	AppStore    *AppStoreTarget `json:"appStore,omitempty"`
	RestartOf   string          `json:"restartOf,omitempty"`
}
type Result struct {
	RecoveryPath     string `json:"recoveryPath,omitempty"`
	BackupPath       string `json:"backupPath,omitempty"`
	PlanID           string `json:"planID"`
	Status           string `json:"status"`
	Reason           string `json:"reason"`
	Executed         bool   `json:"executed"`
	CommandSucceeded bool   `json:"commandSucceeded"`
	ObservedVersion  string `json:"observedVersion,omitempty"`
	ObservedBuild    string `json:"observedBuild,omitempty"`
}

// Client is injected by application code only. The native implementation independently
// authenticates its caller, asks for exact consent, and revalidates on the root side.
type Client interface {
	Check(context.Context, Request) (Result, error)
	Execute(context.Context, Request, string) (Result, error)
}

func Confirmation(request Request) string {
	data, _ := json.Marshal(request)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
func Decode(r io.Reader) (Request, error) {
	var request Request
	d := json.NewDecoder(io.LimitReader(r, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&request); err != nil {
		return request, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return request, errors.New("trailing privileged request data")
	}
	return request, request.Validate()
}
func (r Request) Validate() error {
	if r.OperationID != "" && !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(r.OperationID) {
		return errors.New("invalid operation ID")
	}
	count := 0
	if r.Package != nil {
		count++
	}
	if r.System != nil {
		count++
	}
	if r.AppStore != nil {
		count++
	}
	if r.RestartOf != "" {
		count++
	}
	if count != 1 {
		return errors.New("one privileged target required")
	}
	if r.Kind == "package" && r.Package != nil || r.Kind == "system" && r.System != nil || r.Kind == "app_store_update" && r.AppStore != nil || r.Kind == "restart" && r.RestartOf != "" {
		return nil
	}
	return errors.New("unsupported privileged operation")
}
