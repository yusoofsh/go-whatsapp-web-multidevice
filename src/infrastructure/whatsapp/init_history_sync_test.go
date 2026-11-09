package whatsapp

import (
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/stretchr/testify/require"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// useHistorySyncSettings swaps in a copy of the shared DeviceProps and the
// full-sync settings, restoring both when the test ends.
func useHistorySyncSettings(t *testing.T, enabled bool, days, sizeMB uint32) *waCompanionReg.DeviceProps {
	t.Helper()
	oldProps := store.DeviceProps
	oldEnabled, oldDays, oldSize := config.AppHistoryFullSync, config.AppHistoryFullSyncDays, config.AppHistoryFullSyncSizeMB
	t.Cleanup(func() {
		store.DeviceProps = oldProps
		config.AppHistoryFullSync, config.AppHistoryFullSyncDays, config.AppHistoryFullSyncSizeMB = oldEnabled, oldDays, oldSize
	})
	store.DeviceProps = proto.Clone(oldProps).(*waCompanionReg.DeviceProps)
	config.AppHistoryFullSync, config.AppHistoryFullSyncDays, config.AppHistoryFullSyncSizeMB = enabled, days, sizeMB
	return store.DeviceProps
}

func TestConfigureHistorySyncPropsDisabledKeepsWhatsmeowLimits(t *testing.T) {
	props := useHistorySyncSettings(t, false, 30, 512)
	props.RequireFullSync = proto.Bool(true)
	before := proto.Clone(props.GetHistorySyncConfig())

	configureHistorySyncProps()

	require.NotNil(t, props.RequireFullSync)
	require.False(t, props.GetRequireFullSync(), "a disabled setting must clear an earlier full-sync request")
	require.True(t, proto.Equal(before, props.GetHistorySyncConfig()), "limits must not change while full sync is off")
}

func TestConfigureHistorySyncPropsEnabledSetsLimits(t *testing.T) {
	props := useHistorySyncSettings(t, true, 30, 512)
	historyConfig := props.GetHistorySyncConfig()
	require.NotNil(t, historyConfig, "pinned whatsmeow ships a default HistorySyncConfig")
	supportCallLogHistory := historyConfig.GetSupportCallLogHistory()

	configureHistorySyncProps()

	require.True(t, props.GetRequireFullSync())
	require.Same(t, historyConfig, props.GetHistorySyncConfig(), "the default config is updated in place")
	require.Equal(t, uint32(30), historyConfig.GetFullSyncDaysLimit())
	require.Equal(t, uint32(512), historyConfig.GetFullSyncSizeMbLimit())
	require.Equal(t, supportCallLogHistory, historyConfig.GetSupportCallLogHistory(), "unrelated defaults are kept")
}

func TestConfigureHistorySyncPropsZeroLimitsKeepDefaults(t *testing.T) {
	props := useHistorySyncSettings(t, true, 0, 0)
	props.HistorySyncConfig.FullSyncDaysLimit = proto.Uint32(11)
	props.HistorySyncConfig.FullSyncSizeMbLimit = nil

	configureHistorySyncProps()

	require.True(t, props.GetRequireFullSync())
	require.Equal(t, uint32(11), props.GetHistorySyncConfig().GetFullSyncDaysLimit())
	require.Nil(t, props.GetHistorySyncConfig().FullSyncSizeMbLimit)
}

func TestConfigureHistorySyncPropsAllocatesMissingConfig(t *testing.T) {
	props := useHistorySyncSettings(t, true, 7, 0)
	props.HistorySyncConfig = nil

	configureHistorySyncProps()

	require.NotNil(t, props.GetHistorySyncConfig())
	require.Equal(t, uint32(7), props.GetHistorySyncConfig().GetFullSyncDaysLimit())
	require.Nil(t, props.GetHistorySyncConfig().FullSyncSizeMbLimit)
}

func TestConfigureDevicePropsAppliesHistorySync(t *testing.T) {
	props := useHistorySyncSettings(t, true, 90, 0)

	configureDeviceProps()

	require.True(t, props.GetRequireFullSync())
	require.Equal(t, uint32(90), props.GetHistorySyncConfig().GetFullSyncDaysLimit())
}
