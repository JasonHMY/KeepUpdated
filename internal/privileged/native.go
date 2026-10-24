package privileged

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type NativeClient struct{}

func (NativeClient) Check(ctx context.Context, request Request) (Result, error) {
	return call(ctx, "check", request, "")
}
func (NativeClient) Execute(ctx context.Context, request Request, confirm string) (Result, error) {
	if request.OperationID == "" {
		return Result{}, errors.New("durable operation ID required before execution")
	}
	if confirm != Confirmation(request) {
		return Result{}, errors.New("exact privileged request confirmation required")
	}
	return call(ctx, "execute", request, confirm)
}

func bridge(ctx context.Context, requireDeveloperID bool) (string, string) {
	if runtime.GOOS != "darwin" {
		return "", "macos_required"
	}
	executable, err := os.Executable()
	if err != nil {
		return "", "native_bridge_not_packaged"
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", "native_bridge_not_packaged"
	}
	contents := filepath.Dir(filepath.Dir(executable))
	if filepath.Base(contents) != "Contents" || filepath.Base(filepath.Dir(executable)) != "Resources" {
		return "", "native_bridge_not_packaged"
	}
	path := filepath.Join(contents, "MacOS", "KeepUpdatedAuthorization")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 {
		return "", "native_bridge_not_packaged"
	}
	// The sealed parent bundle binds this bridge to the packaged CLI. Never fall
	// back to PATH, sudo, AppleScript, an environment override or a remote helper.
	args := []string{"--verify", "--deep", "--strict"}
	if requireDeveloperID {
		args = append(args, "-R", "=anchor apple generic")
	}
	args = append(args, filepath.Dir(contents))
	command := exec.CommandContext(ctx, "/usr/bin/codesign", args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	if command.Run() != nil {
		if requireDeveloperID {
			return "", "developer_signature_required"
		}
		return "", "native_bridge_signature_invalid"
	}
	return path, ""
}
func call(ctx context.Context, verb string, request Request, confirm string) (Result, error) {
	if err := request.Validate(); err != nil {
		return Result{}, err
	}
	path, reason := bridge(ctx, verb == "execute")
	if reason != "" {
		return Result{PlanID: request.OperationID, Status: "blocked", Reason: reason}, nil
	}
	data, err := json.Marshal(request)
	if err != nil || len(data) > 65536 {
		return Result{}, errors.New("privileged request exceeds limit")
	}
	args := []string{verb}
	if verb == "execute" {
		args = append(args, "--confirm", confirm)
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Stdin = bytes.NewReader(data)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	var output limitedOutput
	command.Stdout = &output
	runErr := command.Run()
	expectedID := ""
	if verb == "execute" {
		expectedID = request.OperationID
	}
	return decodeResponse(output.Bytes(), output.overflow, runErr, expectedID)
}

func decodeResponse(data []byte, overflow bool, runErr error, operationID string) (Result, error) {
	unknown := Result{PlanID: operationID, Status: "verification_unknown", Reason: "native_response_unavailable_do_not_retry"}
	if overflow {
		return unknown, errors.New("native authorization response exceeds limit")
	}
	var result Result
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil {
		return unknown, errors.New("native authorization result unavailable; query the operation ID")
	}
	var extra any
	if d.Decode(&extra) != io.EOF || operationID != "" && result.PlanID != operationID {
		return unknown, errors.New("ambiguous native authorization response")
	}
	if runErr != nil && result.Status != "blocked" && result.Status != "cancelled" {
		return result, runErr
	}
	return result, nil
}

type limitedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > 65536-b.Len() {
		p = p[:65536-b.Len()]
		b.overflow = true
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}

// Control exposes only service registration and read-only recovery. Update execution
// is reachable through ordinary manage.Apply with its complete approved plan.
func Control(ctx context.Context, args []string, out, errOut io.Writer) int {
	valid := len(args) == 1 && (args[0] == "status" || args[0] == "register-helper") || len(args) == 2 && args[0] == "operation-status"
	if !valid {
		return 2
	}
	path, reason := bridge(ctx, args[0] != "status")
	if reason != "" {
		_ = json.NewEncoder(out).Encode(Result{Status: "blocked", Reason: reason})
		return 1
	}
	command := exec.CommandContext(ctx, path, args...)
	command.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LC_ALL=C"}
	command.Stdout, command.Stderr = out, errOut
	if command.Run() != nil {
		return 1
	}
	return 0
}
