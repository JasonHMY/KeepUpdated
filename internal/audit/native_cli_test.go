package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeUVDiscoveryIsStaticAndRejectsSourcePinAndPrefix(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, ".local/bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(UVReceiptPath(home)), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "executed")
	for _, name := range []string{"uv", "uvx"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\ntouch "+marker+"\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	r := UVReceipt{Binaries: []string{"uv", "uvx"}, InstallLayout: "flat", InstallPrefix: bin, Version: "0.13.0"}
	r.Provider.Source = "cargo-dist"
	r.Source.AppName = "uv"
	r.Source.Name = "uv"
	r.Source.Owner = "astral-sh"
	r.Source.ReleaseType = "github"
	for _, mode := range []string{"valid", "source", "pin", "prefix", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			copy := r
			if mode == "source" {
				copy.Source.Owner = "another-owner"
			}
			if mode == "prefix" {
				copy.InstallPrefix = home
			}
			data, _ := json.Marshal(copy)
			if mode == "pin" {
				data = append(data[:len(data)-1], []byte(`,"pinned":true}`)...)
			}
			if err := os.WriteFile(UVReceiptPath(home), data, 0644); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(bin, "uv")
			if mode == "symlink" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("uvx", file); err != nil {
					t.Fatal(err)
				}
			}
			scanner := New(Config{Home: home, Path: bin})
			scanner.executables()
			found := false
			for _, i := range scanner.Report.Items {
				if i.Name == "uv" {
					found = true
					if mode == "valid" && i.Owner != "uv-standalone" {
						t.Fatal(i)
					}
					if mode != "valid" && i.Owner == "uv-standalone" {
						t.Fatal("unsupported installation accepted", i)
					}
				}
			}
			if !found {
				t.Fatal("executable absent")
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("scan executed CLI")
			}
		})
	}
}
