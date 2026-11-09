package cmd

import (
	"os"
	"os/exec"
	"strconv"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/require"
)

func TestHistoryFullSyncConfigFromEnvironment(t *testing.T) {
	// Each case runs initEnvConfig in its own process so Viper and the config
	// globals start clean.
	if os.Getenv("GOWA_FULL_SYNC_CONFIG_HELPER") == "1" {
		const priorDays, priorSize = 7, 9
		config.AppHistoryFullSync = false
		config.AppHistoryFullSyncDays = priorDays
		config.AppHistoryFullSyncSizeMB = priorSize
		initEnvConfig()

		wantEnabled, err := strconv.ParseBool(os.Getenv("GOWA_FULL_SYNC_WANT_ENABLED"))
		require.NoError(t, err)
		wantDays, err := strconv.ParseUint(os.Getenv("GOWA_FULL_SYNC_WANT_DAYS"), 10, 32)
		require.NoError(t, err)
		wantSize, err := strconv.ParseUint(os.Getenv("GOWA_FULL_SYNC_WANT_SIZE"), 10, 32)
		require.NoError(t, err)
		require.Equal(t, wantEnabled, config.AppHistoryFullSync)
		require.Equal(t, uint32(wantDays), config.AppHistoryFullSyncDays)
		require.Equal(t, uint32(wantSize), config.AppHistoryFullSyncSizeMB)
		return
	}

	for _, tc := range []struct {
		name                string
		enabled, days, size string
		wantEnabled         bool
		wantDays, wantSize  uint32
	}{
		{name: "unset keeps prior values", wantDays: 7, wantSize: 9},
		{name: "enabled with limits", enabled: "true", days: "30", size: "512", wantEnabled: true, wantDays: 30, wantSize: 512},
		{name: "surrounding spaces are trimmed", days: " 45 ", size: "\t64 ", wantDays: 45, wantSize: 64},
		{name: "zero is accepted as no limit", days: "0", size: "0", wantDays: 0, wantSize: 0},
		{name: "explicit false", enabled: "false", days: "1", size: "1", wantDays: 1, wantSize: 1},
		{name: "negative falls back", days: "-1", size: "-5", wantDays: 7, wantSize: 9},
		{name: "overflow falls back", days: "4294967296", size: "99999999999", wantDays: 7, wantSize: 9},
		{name: "maximum uint32 is accepted", days: "4294967295", wantDays: 4294967295, wantSize: 9},
		{name: "non-numeric falls back", days: "thirty", size: "1.5", wantDays: 7, wantSize: 9},
		{name: "blank falls back", days: "   ", size: " ", wantDays: 7, wantSize: 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHistoryFullSyncConfigFromEnvironment$")
			cmd.Dir = t.TempDir()
			cmd.Env = append(os.Environ(),
				"GOWA_FULL_SYNC_CONFIG_HELPER=1",
				"GOWA_FULL_SYNC_WANT_ENABLED="+strconv.FormatBool(tc.wantEnabled),
				"GOWA_FULL_SYNC_WANT_DAYS="+strconv.FormatUint(uint64(tc.wantDays), 10),
				"GOWA_FULL_SYNC_WANT_SIZE="+strconv.FormatUint(uint64(tc.wantSize), 10))
			for key, value := range map[string]string{
				"APP_HISTORY_FULL_SYNC":         tc.enabled,
				"APP_HISTORY_FULL_SYNC_DAYS":    tc.days,
				"APP_HISTORY_FULL_SYNC_SIZE_MB": tc.size,
			} {
				if value != "" {
					cmd.Env = append(cmd.Env, key+"="+value)
				}
			}
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
		})
	}
}
