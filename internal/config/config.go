// Package config keeps the settings of note4win in
// %APPDATA%\note4win\settings.json.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Settings is everything that is remembered between runs: what Notepad
// keeps in the registry.
type Settings struct {
	FontFace    string `json:"fontFace"`
	FontSize    int    `json:"fontSize"` // tenths of a point
	FontWeight  int    `json:"fontWeight"`
	FontItalic  bool   `json:"fontItalic"`
	FontCharSet int    `json:"fontCharSet"`

	WordWrap  bool `json:"wordWrap"`
	StatusBar bool `json:"statusBar"`

	// The window, in pixels; a width of 0 lets Windows choose.
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`

	// Page setup; the margins are in thousandths of an inch.
	Header       string `json:"header"`
	Footer       string `json:"footer"`
	MarginLeft   int    `json:"marginLeft"`
	MarginTop    int    `json:"marginTop"`
	MarginRight  int    `json:"marginRight"`
	MarginBottom int    `json:"marginBottom"`

	MatchCase  bool `json:"matchCase"`
	WrapAround bool `json:"wrapAround"`
}

// Default returns the settings of a first run, which are Notepad's.
func Default() *Settings {
	return &Settings{
		FontFace: "Consolas", FontSize: 110, FontWeight: 400, FontCharSet: 1,
		StatusBar: true,
		Header:    "&f", Footer: "Page &p",
		MarginLeft: 750, MarginTop: 1000, MarginRight: 750, MarginBottom: 1000,
	}
}

// Dir returns the folder of the settings file.
func Dir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "note4win"), nil
}

// Load reads the settings; without a readable file it returns the defaults.
func Load(dir string) *Settings {
	s := Default()
	data, err := os.ReadFile(filepath.Join(dir, "settings.json"))
	if err != nil {
		return s
	}
	if json.Unmarshal(data, s) != nil {
		return Default()
	}
	if s.FontFace == "" || s.FontSize < 10 || s.FontSize > 7200 {
		d := Default()
		s.FontFace, s.FontSize, s.FontWeight, s.FontItalic, s.FontCharSet = d.FontFace, d.FontSize, d.FontWeight, false, d.FontCharSet
	}
	return s
}

// Save writes the settings.
func (s *Settings) Save(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "settings.json.tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "settings.json"))
}
