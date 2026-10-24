package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestManifestCompatibility(t *testing.T) {
	read := func(path string) map[string]any {
		t.Helper()
		b, e := os.ReadFile(filepath.Join("../../plugin", path))
		if e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		if e = json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	portable := read("plugin.json")
	if portable["$schema"] != "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json" {
		t.Fatal("portable schema")
	}
	ext := portable["extensions"].(map[string]any)["com.openai"].(map[string]any)
	if ext["requires_local_executor"] != true {
		t.Fatal("local executor required")
	}
	presentation := ext["interface"].(map[string]any)
	if len([]rune(presentation["shortDescription"].(string))) > 30 {
		t.Fatal("subtitle exceeds 30 characters")
	}
	compatibility := read(".codex-plugin/plugin.json")
	delete(portable, "$schema")
	delete(portable, "extensions")
	for k, v := range ext {
		portable[k] = v
	}
	portable["skills"] = "./skills/"
	portable["mcpServers"] = "./.mcp.json"
	if !reflect.DeepEqual(portable, compatibility) {
		t.Fatal("Codex identity/presentation differs from portable manifest")
	}
	mcp := read("mcp.json")
	if mcp["$schema"] != "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json" {
		t.Fatal("MCP schema")
	}
	delete(mcp, "$schema")
	servers := mcp["mcpServers"].(map[string]any)
	server := servers["keepupdated"].(map[string]any)
	// The portable loader rejects ${PLUGIN_ROOT} in command, unlike legacy loading.
	if server["command"] != "./KeepUpdatedAgent.app/Contents/Resources/keepupdated" || server["cwd"] != "${PLUGIN_ROOT}" {
		t.Fatal("portable command must be a contained ./ path; placeholders belong in cwd/args")
	}
	server["command"] = "${PLUGIN_ROOT}/KeepUpdatedAgent.app/Contents/Resources/keepupdated"
	if !reflect.DeepEqual(mcp, read(".mcp.json")) {
		t.Fatal("MCP compatibility differs")
	}
}
