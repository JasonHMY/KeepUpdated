package resolve

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/JasonHMY/KeepUpdated/internal/audit"
	"github.com/JasonHMY/KeepUpdated/internal/manage"
)

func TestAppStoreProviderIsConnectedWithoutManagerQuery(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Example.app")
	if err = os.Mkdir(path, 0755); err != nil {
		t.Fatal(err)
	}
	item := audit.Item{ID: "store", Kind: "application", Path: path, RealPath: path, Identity: "com.example.store", Owner: "app-store", Source: "app-store", Version: "1.0"}
	resolver := Resolver{Options: manage.Options{Home: root, AppRoots: []string{root}, ManagerPaths: map[string]string{"mas": filepath.Join(root, "missing-mas")}}}
	targets, err := resolver.Resolve(context.Background(), audit.Report{Items: []audit.Item{item}}, manage.Selection{Action: "update", IDs: []string{item.ID}})
	if err != nil || targets[item.ID].Provider != "app-store" || targets[item.ID].Reason != "app_store_manual_update" {
		t.Fatal(targets, err)
	}
}
