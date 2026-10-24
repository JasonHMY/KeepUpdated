package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPrepareRejectsWrongArchiveBeforeExtractionOrOutput(t *testing.T) {
	for _, arch := range []string{"arm64", "x86_64"} {
		t.Run(arch, func(t *testing.T) {
			parent := t.TempDir()
			if err := os.Chmod(parent, 0700); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(parent, "manager")
			_, pin, err := pinned(arch)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != pin.URL {
					t.Fatal("unpinned download", r.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("incorrect archive content")), Header: http.Header{}}, nil
			})}
			if _, err = prepare(context.Background(), arch, out, client); err == nil || err.Error() != "official archive SHA-256 mismatch" {
				t.Fatal("incorrect digest was not rejected", err)
			}
			files, err := os.ReadDir(parent)
			if err != nil || len(files) != 0 {
				t.Fatal("incorrect archive reached staging", files, err)
			}
		})
	}
}
