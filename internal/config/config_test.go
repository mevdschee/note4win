package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if got := Load(dir); *got != *Default() {
		t.Fatalf("without a file: %+v", got)
	}
	s := Default()
	s.FontFace, s.FontSize, s.WordWrap, s.Width, s.Header = "Courier New", 140, true, 800, "&l&f"
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir); *got != *s {
		t.Fatalf("got %+v, want %+v", got, s)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{broken"), 0o600)
	if got := Load(dir); *got != *Default() {
		t.Fatalf("with a broken file: %+v", got)
	}
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"fontFace":"","wordWrap":true}`), 0o600)
	if got := Load(dir); got.FontFace != "Consolas" || !got.WordWrap {
		t.Fatalf("with a bad font: %+v", got)
	}
}
