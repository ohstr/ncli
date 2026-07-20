package ncli

import (
	"fmt"
	"path/filepath"

	"github.com/ohstr/ncli/cli/common"
	"github.com/ohstr/ncli/client"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version and app data location information",
	Long: `Print build version information along with the on-disk locations ncli
reads and writes: the app data directory, prefs file, vault file, and log
directory. --json prints the same information as structured JSON, for
scripts or an AI agent.`,
	// version just reads embedded build info, so it skips the root's
	// PersistentPreRun (config loading, log dir/crash log setup) instead
	// of inheriting it like every other subcommand.
	PersistentPreRun: func(cmd *cobra.Command, args []string) {},
	Run: func(cmd *cobra.Command, args []string) {
		info := common.ReadBuildInfo()

		// Best-effort: only used here to honor a configured log_dir
		// override, same as InitConfig's own resolution -- but without
		// InitConfig's side effects (mkdir, crash log path, lumberjack
		// setup), which version intentionally skips. A missing/invalid
		// config file is not this command's concern.
		common.LoadViperConfig(cfgFile)
		logDir := viper.GetString("log_dir")
		if logDir == "" {
			logDir = filepath.Join(common.AppConfigDir(), defaultLogDirName)
		}

		jsonMode, _ := cmd.Flags().GetBool("json")
		if jsonMode {
			common.PrintJSON(map[string]string{
				"ncli_version":   info.Version,
				"nmilat_version": info.NmilatVersion,
				"software":       info.Software,
				"app_data_dir":   common.AppConfigDir(),
				"prefs_path":     client.PrefsPath(),
				"vault_path":     client.VaultPath(),
				"log_dir":        logDir,
			})
			return
		}

		fmt.Println("ncli:", info.Version)
		fmt.Println("nmilat:", info.NmilatVersion)
		fmt.Println("app data dir:", common.AppConfigDir())
		fmt.Println("prefs file:  ", client.PrefsPath())
		fmt.Println("vault file:  ", client.VaultPath())
		fmt.Println("log dir:     ", logDir)
	},
}

func init() {
	RootCmd.AddCommand(versionCmd)
}
