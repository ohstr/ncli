package common

import (
	"os"
	"path/filepath"
)

// AppDirName is ncli's own subdirectory within the OS's per-user
// application directory.
const AppDirName = ".ncli"

// AppConfigDir returns the OS-appropriate per-user application directory
// for ncli -- %AppData% on Windows, ~/Library/Application Support on
// macOS, $XDG_CONFIG_HOME (or ~/.config) on Linux -- joined with ncli's
// own .ncli subdirectory. It's the one base directory everything ncli
// writes outside a project lives under: prefs.yaml, the CLI's log file,
// and its crash log. Falls back to the home directory, then the working
// directory, if the platform's config directory can't be determined
// (e.g. neither $XDG_CONFIG_HOME nor $HOME set).
func AppConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			home, _ = os.Getwd()
		}
		dir = home
	}
	return filepath.Join(dir, AppDirName)
}
