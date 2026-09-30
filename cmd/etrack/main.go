// Command etrack is the entry point of the ETrack terminal application:
// it resolves the database path, opens (and migrates) SQLite, and hands the
// root Bubble Tea model to the program.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"etrack/internal/storage"
	"etrack/internal/ui"
)

// defaultDBPath returns ~/.local/share/etrack/etrack.db (XDG-aware),
// falling back to a hidden file in the user's home directory.
func defaultDBPath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "etrack", "etrack.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "etrack.db" // last resort: current directory
	}
	return filepath.Join(home, ".local", "share", "etrack", "etrack.db")
}

func main() {
	dbPath := flag.String("db", "", "path to the SQLite database (default: "+defaultDBPath()+")")
	flag.Parse()

	path := *dbPath
	if path == "" {
		path = defaultDBPath()
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "etrack: cannot create data dir: %v\n", err)
			os.Exit(1)
		}
	}

	db, err := storage.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "etrack: cannot open database: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	p := tea.NewProgram(ui.New(db), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "etrack: runtime error: %v\n", err)
		os.Exit(1)
	}
}
