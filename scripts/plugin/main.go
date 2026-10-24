// Development-only package verification; the installed plugin needs no Go runtime.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type launch struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
}
type configuration struct {
	Schema  string            `json:"$schema"`
	Servers map[string]launch `json:"mcpServers"`
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "Usage: plugin EXTRACTED_PLUGIN")
		os.Exit(2)
	}
	if err := verify(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("Plugin manifests, paths, Skill, icon, stdio initialization, eight tools and read-only call verified.")
}
func verify(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// Reject links and unexpected outer files; the Agent verifier owns sealed content.
	expected := map[string]bool{"skills/keepupdated/SKILL.md": true, "assets/logo.png": true, "KeepUpdatedAgent_PAYLOAD.json": true}
	err = filepath.WalkDir("plugin", func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel("plugin", path)
		if e != nil {
			return e
		}
		expected[rel] = true
		want, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		got, e := os.ReadFile(filepath.Join(root, rel))
		if e != nil {
			return e
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("plugin component differs: %s", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("unexpected symlink: %s", path)
		}
		if d.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if strings.HasPrefix(rel, "KeepUpdatedAgent.app/") {
			return nil
		}
		if !expected[rel] {
			return fmt.Errorf("unexpected plugin file: %s", rel)
		}
		return nil
	})
	if err != nil {
		return err
	}
	skill, err := os.ReadFile("skills/keepupdated/SKILL.md")
	if err != nil {
		return err
	}
	packaged, err := os.ReadFile(filepath.Join(root, "skills/keepupdated/SKILL.md"))
	if err != nil {
		return err
	}
	if !bytes.Equal(skill, packaged) {
		return fmt.Errorf("top-level Skill differs from repository")
	}
	icon, err := os.Open(filepath.Join(root, "assets/logo.png"))
	if err != nil {
		return err
	}
	cfg, err := png.DecodeConfig(icon)
	icon.Close()
	if err != nil {
		return err
	}
	info, err := os.Stat(filepath.Join(root, "assets/logo.png"))
	if err != nil {
		return err
	}
	if cfg.Width != cfg.Height || cfg.Width < 48 || cfg.Width > 4096 || info.Size() > 5<<20 {
		return fmt.Errorf("invalid plugin icon dimensions or size")
	}
	for _, path := range []string{"skills/keepupdated/scripts/run.sh", "KeepUpdatedAgent.app/Contents/Resources/keepupdated", "KeepUpdatedAgent.app/Contents/MacOS/KeepUpdatedAuthorization"} {
		info, e := os.Stat(filepath.Join(root, path))
		if e != nil {
			return e
		}
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("missing executable mode: %s", path)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	home, err := os.MkdirTemp("", "keepupdated-plugin-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	// No inherited user configuration, executable override, credentials or managers.
	env := []string{"HOME=" + home, "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "TMPDIR=" + home}
	cmd := exec.CommandContext(ctx, "/bin/bash", filepath.Join(root, "skills/keepupdated/scripts/run.sh"), "version", "--format", "json")
	cmd.Env = env
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("packaged Skill: %w", err)
	}
	var version map[string]any
	if err = json.Unmarshal(out, &version); err != nil {
		return err
	}
	if version["version"] != "0.1.2" || version["schema"] != "keepupdated/v1" || version["plan_schema"] != "keepupdated/plan/v1" || version["apply_schema"] != "keepupdated/apply/v1" || version["recent_operation_schema"] != "keepupdated/recent-operation/v1" {
		return fmt.Errorf("unexpected Skill version/schema")
	}
	for _, file := range []string{"mcp.json", ".mcp.json"} {
		b, e := os.ReadFile(filepath.Join(root, file))
		if e != nil {
			return e
		}
		var c configuration
		if e = json.Unmarshal(b, &c); e != nil {
			return e
		}
		if len(c.Servers) != 1 {
			return fmt.Errorf("expected one MCP server")
		}
		l, ok := c.Servers["keepupdated"]
		expectedCommand := "${PLUGIN_ROOT}/KeepUpdatedAgent.app/Contents/Resources/keepupdated"
		if file == "mcp.json" {
			expectedCommand = "./KeepUpdatedAgent.app/Contents/Resources/keepupdated"
		}
		if !ok || l.Type != "stdio" || l.Command != expectedCommand || l.Cwd != "${PLUGIN_ROOT}" || len(l.Args) != 1 || l.Args[0] != "mcp" {
			return fmt.Errorf("invalid portable launch in %s", file)
		}
		if file == "mcp.json" && c.Schema != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" {
			return fmt.Errorf("missing MCP schema")
		}
		if e = smoke(ctx, root, env, l, version); e != nil {
			return fmt.Errorf("%s: %w", file, e)
		}
	}
	return nil
}
func smoke(ctx context.Context, root string, env []string, l launch, version map[string]any) error {
	command := strings.ReplaceAll(l.Command, "${PLUGIN_ROOT}", root)
	if strings.HasPrefix(command, "./") {
		command = filepath.Join(root, command)
	}
	cmd := exec.CommandContext(ctx, command, l.Args...)
	cmd.Dir = root
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "keepupdated-package-check", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}, nil)
	if err != nil {
		return fmt.Errorf("initialize: %w (%s)", err, stderr.String())
	}
	defer session.Close()
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		return err
	}
	expected := map[string]bool{"get_capabilities": true, "scan_inventory": true, "check_updates": true, "list_items": true, "get_item": true, "preview_changes": true, "request_execution": true, "get_operation": true}
	if len(list.Tools) != len(expected) {
		return fmt.Errorf("expected eight tools, got %d", len(list.Tools))
	}
	for _, tool := range list.Tools {
		if !expected[tool.Name] {
			return fmt.Errorf("unexpected tool: %s", tool.Name)
		}
		delete(expected, tool.Name)
		if tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || tool.Annotations.OpenWorldHint == nil {
			return fmt.Errorf("missing annotations: %s", tool.Name)
		}
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_capabilities", Arguments: map[string]any{}})
	if err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("capabilities failed")
	}
	b, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	var caps map[string]any
	if err = json.Unmarshal(b, &caps); err != nil {
		return err
	}
	for _, key := range []string{"schema", "plan_schema", "apply_schema"} {
		if caps[key] != version[key] {
			return fmt.Errorf("MCP %s differs from the matching CLI", key)
		}
	}
	if caps["version"] != version["version"] || caps["source_commit"] != version["commit"] || caps["transport"] != "stdio" || caps["platform"] != "darwin" || caps["arch"] != "arm64" || caps["approval_available"] != true || caps["privileged_execution"] != "blocked_use_desktop_or_manual" {
		return fmt.Errorf("unexpected capabilities: %s", b)
	}
	return nil
}
