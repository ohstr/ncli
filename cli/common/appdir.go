package common

import "github.com/ohstr/ncli/appdir"

// The implementation lives in the leaf package appdir, so the vault and prefs
// stores can resolve the same directory without importing this package (and
// with it viper and zerolog). These stay as the CLI's spelling of it.

// AppDirName is ncli's own subdirectory within the OS's per-user
// application directory.
const AppDirName = appdir.Name

// AppConfigDir returns the OS-appropriate per-user application directory for
// ncli. See appdir.Config.
func AppConfigDir() string { return appdir.Config() }
