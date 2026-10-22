package uiinstall

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstall(t *testing.T) {
	src, dst := t.TempDir(), t.TempDir()
	js := filepath.Join(src, "resources", "zea", "extension-zea.js")
	if err := os.MkdirAll(filepath.Dir(js), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(js, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dst, "resources", "zea", "extension-zea.js")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := Install(src, dst)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"resources/zea/extension-zea.js"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("copied = %v", got)
	}
	if b, _ := os.ReadFile(old); string(b) != "new" {
		t.Fatalf("content = %q", b)
	}
}

func TestInstallEmpty(t *testing.T) {
	if _, err := Install(t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("expected an error for an empty source")
	}
	if _, err := Install(filepath.Join(t.TempDir(), "missing"), t.TempDir()); err == nil {
		t.Fatal("expected an error for a missing source")
	}
}
