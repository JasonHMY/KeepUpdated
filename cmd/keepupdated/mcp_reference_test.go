package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestMCPReferenceCLIRequiresExactScopeBeforeScanning(t *testing.T) {
	for _, args := range [][]string{
		{"--action", "update", "--all", "--special", "--mcp-reference", "example@2.0.0"},
		{"--action", "uninstall", "--id", "one", "--special", "--mcp-reference", "example@2.0.0"},
		{"--action", "update", "--id", "one", "--mcp-reference", "example@2.0.0"},
		{"--action", "update", "--id", "one", "--id", "two", "--special", "--mcp-reference", "example@2.0.0"},
	} {
		var out, errOut bytes.Buffer
		if code := runManage("plan", args, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "one explicit ID") {
			t.Fatal(code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := runManage("plan", []string{"--help"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "mcp-reference") || !strings.Contains(errOut.String(), "configuration only") {
		t.Fatal(code, errOut.String())
	}
}

func TestMCPCandidateCLIRequiresReferenceAndBindingBeforeScanning(t *testing.T) {
	for _, args := range [][]string{
		{"--action", "update", "--id", "one", "--special", "--mcp-candidate", strings.Repeat("a", 64)},
		{"--action", "update", "--id", "one", "--special", "--mcp-reference", "example@2.0.0", "--mcp-candidate", "short"},
	} {
		var out, errOut bytes.Buffer
		if code := runManage("plan", args, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "exact reference and candidate ID") {
			t.Fatal(code, out.String(), errOut.String())
		}
	}
}
