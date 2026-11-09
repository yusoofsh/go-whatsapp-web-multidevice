package mcp

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"go.mau.fi/whatsmeow/types"
)

const phoneVerificationFileEnv = "APP_HISTORY_PHONE_VERIFICATION_FILE"

type phoneVerificationChat struct {
	PhoneComparison string `json:"phone_comparison"`
	CheckedAt       string `json:"checked_at"`
	Baseline        struct {
		OldestUTC string `json:"oldest_utc"`
	} `json:"baseline"`
}

type phoneVerificationManifest struct {
	Account   string                           `json:"account"`
	UpdatedAt string                           `json:"updated_at"`
	Chats     map[string]phoneVerificationChat `json:"chats"`
}

// applyPhoneVerification attaches an operator-recorded phone comparison for
// chatJID to resp. Any missing, unreadable or mismatched manifest leaves resp
// untouched, because the check is advisory and must never fail a history read.
func applyPhoneVerification(resp *chat.HistoryResponse, accountJID, chatJID string) {
	if resp == nil || strings.TrimSpace(chatJID) == "" {
		return
	}
	path := strings.TrimSpace(os.Getenv(phoneVerificationFileEnv))
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var manifest phoneVerificationManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return
	}
	if canonicalJID(manifest.Account) == "" || canonicalJID(manifest.Account) != canonicalJID(accountJID) {
		return
	}
	entry, ok := manifest.Chats[strings.TrimSpace(chatJID)]
	if !ok || entry.Baseline.OldestUTC == "" {
		return
	}
	timestamp := entry.CheckedAt
	recordedAt := ""
	if timestamp == "" {
		recordedAt = manifest.UpdatedAt
		timestamp = recordedAt
	}
	if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
		return
	}
	oldest, err := parsePhoneTimestamp(entry.Baseline.OldestUTC)
	if err != nil {
		return
	}
	oldestDate := oldest.UTC().Format("2006-01-02")

	verification := &chat.PhoneVerification{
		CheckedAt:       entry.CheckedAt,
		RecordedAt:      recordedAt,
		KnownOldestDate: oldestDate,
		Scope:           "oldest visible message or post boundary compared with the primary phone; does not certify every intervening message",
	}
	switch entry.PhoneComparison {
	case "matches_oldest_visible_date":
		if resp.Coverage == nil || resp.Coverage.MessageCount <= 0 || resp.Coverage.OldestTimestamp == nil ||
			resp.Coverage.OldestTimestamp.UTC().Format("2006-01-02") != oldestDate {
			return
		}
		verification.Status = "oldest_date_verified"
		resp.Completeness = "oldest_date_verified"
	case "not_available_on_phone":
		verification.Status = "not_located_on_phone"
		resp.Completeness = "unverified"
	case "status_retention_only":
		verification.Status = "retention_only"
	default:
		return
	}
	resp.BestEffort = true
	resp.PhoneVerification = verification
}

func parsePhoneTimestamp(value string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05Z07:00"} {
		if parsed, err := time.Parse(layout, strings.TrimSpace(value)); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, os.ErrInvalid
}

func canonicalJID(value string) string {
	jid, err := types.ParseJID(strings.TrimSpace(value))
	if err != nil || jid.IsEmpty() {
		return ""
	}
	return jid.ToNonAD().String()
}
