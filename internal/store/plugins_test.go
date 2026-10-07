package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func pluginOwner(t *testing.T, db *DB) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.CreateUser(ctx, "u1", "tester", "hash"); err != nil {
		t.Fatal(err)
	}
	return "u1"
}

func TestPluginVersionsFilesAndGrants(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	owner := pluginOwner(t, db)

	if _, err := db.CreatePlugin(ctx, "standup", owner, "/tmp/standup"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreatePlugin(ctx, "standup", owner, ""); !errors.Is(err, ErrExists) {
		t.Errorf("a second create of the same id: %v, want ErrExists", err)
	}
	v, err := db.AddPluginVersion(ctx, "standup", NewPluginVersion{Manifest: json.RawMessage(`{"plugin":1}`), Hash: "h1",
		Files: []NewPluginFile{{"pane.html", "text/html", []byte("<p>1</p>")}, {"theme.css", "text/css", []byte(":root{}")}}})
	if err != nil || v != 1 {
		t.Fatalf("v1: %d %v", v, err)
	}
	v, err = db.AddPluginVersion(ctx, "standup", NewPluginVersion{Manifest: json.RawMessage(`{"plugin":1}`), Hash: "h2",
		Files: []NewPluginFile{{"pane.html", "text/html", []byte("<p>2</p>")}, {"theme.css", "text/css", []byte(":root{}")}}})
	if err != nil || v != 2 {
		t.Fatalf("v2: %d %v", v, err)
	}

	p, err := db.PluginByID(ctx, "standup")
	if err != nil || p.InstalledVersion != 0 || p.Enabled {
		t.Fatalf("before install: %+v %v", p, err)
	}
	if err := db.InstallPluginVersion(ctx, "standup", 3, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("installing a version that does not exist: %v", err)
	}
	if err := db.InstallPluginVersion(ctx, "standup", 2, true); err != nil {
		t.Fatal(err)
	}
	p, _ = db.PluginByID(ctx, "standup")
	if p.InstalledVersion != 2 || !p.Enabled {
		t.Errorf("after install: %+v", p)
	}
	ct, data, err := db.PluginFileData(ctx, "standup", 2, "pane.html")
	if err != nil || ct != "text/html" || string(data) != "<p>2</p>" {
		t.Errorf("file: %s %q %v", ct, data, err)
	}
	files, _ := db.PluginFiles(ctx, "standup", 1)
	if len(files) != 2 {
		t.Errorf("v1 files = %v", files)
	}

	if err := db.SetPluginCaps(ctx, "standup", []string{"read:panel", "write:todos"}, "tester"); err != nil {
		t.Fatal(err)
	}
	caps, _ := db.PluginCaps(ctx, "standup")
	if len(caps) != 2 || caps[0].Cap != "read:panel" || caps[0].GrantedBy != "tester" {
		t.Errorf("caps = %+v", caps)
	}
	if err := db.SetPluginCaps(ctx, "standup", nil, "tester"); err != nil {
		t.Fatal(err)
	}
	if caps, _ := db.PluginCaps(ctx, "standup"); len(caps) != 0 {
		t.Errorf("caps after clearing = %+v", caps)
	}

	if err := db.SetPluginSetting(ctx, "standup", "quiet", json.RawMessage(`true`)); err != nil {
		t.Fatal(err)
	}
	if err := db.SetPluginSetting(ctx, "nope", "quiet", json.RawMessage(`true`)); !errors.Is(err, ErrNotFound) {
		t.Errorf("a setting on a plugin that does not exist: %v", err)
	}
	settings, _ := db.PluginSettings(ctx, "standup")
	if string(settings["quiet"]) != "true" {
		t.Errorf("settings = %v", settings)
	}
	if err := db.SetPluginSecret(ctx, "standup", "TOKEN", []byte("sealed")); err != nil {
		t.Fatal(err)
	}
	names, _ := db.PluginSecretNames(ctx, "standup")
	if _, ok := names["TOKEN"]; !ok {
		t.Errorf("secret names = %v", names)
	}

	// Deleting takes everything with it, and the blob a page still uses stays.
	if _, err := db.CreateSharePage(ctx, "pg", owner, "P", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddSharePageVersion(ctx, "pg", NewSharePageVersion{Manifest: json.RawMessage(`{}`),
		Files: []NewSharePageFile{{"index.html", "text/html", []byte("<p>2</p>")}}, Publish: true}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeletePlugin(ctx, "standup"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PluginByID(ctx, "standup"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: %v", err)
	}
	if _, data, err := db.SharePageFileData(ctx, "pg", 1, "index.html"); err != nil || string(data) != "<p>2</p>" {
		t.Errorf("the page's blob went with the plugin: %q %v", data, err)
	}
	if caps, _ := db.PluginCaps(ctx, "standup"); len(caps) != 0 {
		t.Errorf("caps survived the delete")
	}
}

// The sweep that a page's publish runs must not take a plugin's files: both
// tables share the blob store.
func TestAPagePublishDoesNotSweepAPluginsFiles(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	owner := pluginOwner(t, db)
	if _, err := db.CreatePlugin(ctx, "keep", owner, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddPluginVersion(ctx, "keep", NewPluginVersion{Manifest: json.RawMessage(`{}`),
		Files: []NewPluginFile{{"only.html", "text/html", []byte("<p>only the plugin has this</p>")}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSharePage(ctx, "pg", owner, "P", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddSharePageVersion(ctx, "pg", NewSharePageVersion{Manifest: json.RawMessage(`{}`),
		Files: []NewSharePageFile{{"index.html", "text/html", []byte("<p>page</p>")}}, Publish: true}); err != nil {
		t.Fatal(err)
	}
	if _, data, err := db.PluginFileData(ctx, "keep", 1, "only.html"); err != nil || len(data) == 0 {
		t.Errorf("the page's publish swept the plugin's file: %v", err)
	}
}
