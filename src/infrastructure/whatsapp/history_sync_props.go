package whatsapp

import (
	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"go.mau.fi/whatsmeow/proto/waCompanionReg"
	"go.mau.fi/whatsmeow/store"
	"google.golang.org/protobuf/proto"
)

// applyFullHistorySyncProps asks the phone for the FULL message history when a
// device pairs. APP_HISTORY_FULL_SYNC takes precedence over the upstream
// WHATSAPP_FULL_HISTORY_SYNC alias; when neither setting is enabled an earlier
// RequireFullSync request is cleared so a disabled setting always wins.
func applyFullHistorySyncProps(props *waCompanionReg.DeviceProps) {
	switch {
	case config.AppHistoryFullSync:
		applyAppHistorySyncProps(props)
	case config.WhatsappFullHistorySync:
		props.PlatformType = waCompanionReg.DeviceProps_DESKTOP.Enum()
		props.RequireFullSync = proto.Bool(true)
		if props.HistorySyncConfig == nil {
			props.HistorySyncConfig = &waCompanionReg.DeviceProps_HistorySyncConfig{}
		}
		props.HistorySyncConfig.FullSyncDaysLimit = proto.Uint32(config.WhatsappFullHistoryDays)
		props.HistorySyncConfig.FullSyncSizeMbLimit = proto.Uint32(config.WhatsappFullHistorySizeMB)
	default:
		props.RequireFullSync = proto.Bool(false)
	}
}

// applyAppHistorySyncProps implements the APP_HISTORY_FULL_SYNC_* settings used
// by the Lighthouse deployment. RequireFullSync is always written so a disabled
// setting overrides an earlier full-sync request; zero day/size limits keep
// whatsmeow's own defaults.
func applyAppHistorySyncProps(props *waCompanionReg.DeviceProps) {
	requireFullSync := config.AppHistoryFullSync
	props.RequireFullSync = &requireFullSync
	if !requireFullSync {
		return
	}
	historyConfig := props.GetHistorySyncConfig()
	if historyConfig == nil {
		historyConfig = &waCompanionReg.DeviceProps_HistorySyncConfig{}
		props.HistorySyncConfig = historyConfig
	}
	if config.AppHistoryFullSyncDays > 0 {
		days := config.AppHistoryFullSyncDays
		historyConfig.FullSyncDaysLimit = &days
	}
	if config.AppHistoryFullSyncSizeMB > 0 {
		sizeMB := config.AppHistoryFullSyncSizeMB
		historyConfig.FullSyncSizeMbLimit = &sizeMB
	}
}

// configureHistorySyncProps applies the APP_HISTORY_FULL_SYNC_* settings to the
// process-wide DeviceProps.
func configureHistorySyncProps() {
	applyAppHistorySyncProps(store.DeviceProps)
}
