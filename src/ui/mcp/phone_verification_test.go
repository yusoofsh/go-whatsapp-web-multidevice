package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/stretchr/testify/require"
)

const (
	verificationAccount = "628111000111@s.whatsapp.net"
	verificationChat    = "628222000222@s.whatsapp.net"
)

// writeVerificationManifest points APP_HISTORY_PHONE_VERIFICATION_FILE at a
// temp file holding manifest, which is JSON-encoded unless it is a string.
func writeVerificationManifest(t *testing.T, manifest any) {
	t.Helper()
	var raw []byte
	if text, ok := manifest.(string); ok {
		raw = []byte(text)
	} else {
		var err error
		raw, err = json.Marshal(manifest)
		require.NoError(t, err)
	}
	path := filepath.Join(t.TempDir(), "phone-verification.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	t.Setenv(phoneVerificationFileEnv, path)
}

func verificationManifest(comparison, checkedAt, oldestUTC string) map[string]any {
	return map[string]any{
		"account":    "628111000111:7@s.whatsapp.net",
		"updated_at": "2026-10-01T00:00:00Z",
		"chats": map[string]any{
			verificationChat: map[string]any{
				"phone_comparison": comparison,
				"checked_at":       checkedAt,
				"baseline":         map[string]any{"oldest_utc": oldestUTC},
			},
		},
	}
}

func coveredResponse(oldest time.Time, count int64) chat.HistoryResponse {
	return chat.HistoryResponse{
		Completeness: "unknown",
		Coverage:     &chatstorage.ArchiveCoverage{ChatJID: verificationChat, MessageCount: count, OldestTimestamp: &oldest},
	}
}

func TestApplyPhoneVerificationStatuses(t *testing.T) {
	oldest := time.Date(2021, time.May, 4, 23, 30, 0, 0, time.UTC)

	t.Run("matching oldest date verifies the boundary", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("matches_oldest_visible_date", "2026-10-02T08:00:00Z", "2021-05-04 10:00:00+00:00"))
		resp := coveredResponse(oldest, 12)

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.Equal(t, "oldest_date_verified", resp.Completeness)
		require.True(t, resp.BestEffort)
		require.Equal(t, &chat.PhoneVerification{
			Status:          "oldest_date_verified",
			CheckedAt:       "2026-10-02T08:00:00Z",
			KnownOldestDate: "2021-05-04",
			Scope:           "oldest visible message or post boundary compared with the primary phone; does not certify every intervening message",
		}, resp.PhoneVerification)
	})

	t.Run("different oldest date is not claimed", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("matches_oldest_visible_date", "2026-10-02T08:00:00Z", "2021-05-05T00:00:00Z"))
		resp := coveredResponse(oldest, 12)

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.Equal(t, coveredResponse(oldest, 12), resp)
	})

	t.Run("match needs local coverage", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("matches_oldest_visible_date", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
		for name, resp := range map[string]chat.HistoryResponse{
			"no coverage":     {Completeness: "unknown"},
			"no messages":     coveredResponse(oldest, 0),
			"no oldest stamp": {Completeness: "unknown", Coverage: &chatstorage.ArchiveCoverage{ChatJID: verificationChat, MessageCount: 3}},
		} {
			before := resp
			applyPhoneVerification(&resp, verificationAccount, verificationChat)
			require.Equal(t, before, resp, name)
		}
	})

	t.Run("not on phone marks the archive unverified", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("not_available_on_phone", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
		resp := chat.HistoryResponse{Completeness: "unknown"}

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.Equal(t, "unverified", resp.Completeness)
		require.True(t, resp.BestEffort)
		require.Equal(t, "not_located_on_phone", resp.PhoneVerification.Status)
	})

	t.Run("status retention keeps completeness", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("status_retention_only", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
		resp := chat.HistoryResponse{Completeness: "unknown"}

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.Equal(t, "unknown", resp.Completeness)
		require.True(t, resp.BestEffort)
		require.Equal(t, "retention_only", resp.PhoneVerification.Status)
	})

	t.Run("unknown comparison is ignored", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("looks_fine", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z"))
		resp := coveredResponse(oldest, 12)

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.Equal(t, coveredResponse(oldest, 12), resp)
	})

	t.Run("missing checked_at falls back to updated_at", func(t *testing.T) {
		writeVerificationManifest(t, verificationManifest("status_retention_only", "", "2021-05-04T00:00:00Z"))
		resp := chat.HistoryResponse{}

		applyPhoneVerification(&resp, verificationAccount, verificationChat)

		require.NotNil(t, resp.PhoneVerification)
		require.Empty(t, resp.PhoneVerification.CheckedAt)
		require.Equal(t, "2026-10-01T00:00:00Z", resp.PhoneVerification.RecordedAt)
	})
}

func TestApplyPhoneVerificationIgnoresUnusableManifest(t *testing.T) {
	valid := verificationManifest("status_retention_only", "2026-10-02T08:00:00Z", "2021-05-04T00:00:00Z")
	withChat := func(entry map[string]any) map[string]any {
		return map[string]any{"account": valid["account"], "updated_at": valid["updated_at"], "chats": map[string]any{verificationChat: entry}}
	}
	cases := map[string]any{
		"invalid JSON":       "{not json",
		"other account":      map[string]any{"account": "628999000999@s.whatsapp.net", "chats": valid["chats"]},
		"blank account":      map[string]any{"account": " ", "chats": valid["chats"]},
		"chat not listed":    map[string]any{"account": valid["account"], "chats": map[string]any{}},
		"empty baseline":     withChat(map[string]any{"phone_comparison": "status_retention_only", "checked_at": "2026-10-02T08:00:00Z"}),
		"bad checked_at":     withChat(map[string]any{"phone_comparison": "status_retention_only", "checked_at": "yesterday", "baseline": map[string]any{"oldest_utc": "2021-05-04T00:00:00Z"}}),
		"bad updated_at":     map[string]any{"account": valid["account"], "updated_at": "", "chats": map[string]any{verificationChat: map[string]any{"phone_comparison": "status_retention_only", "baseline": map[string]any{"oldest_utc": "2021-05-04T00:00:00Z"}}}},
		"bad oldest":         withChat(map[string]any{"phone_comparison": "status_retention_only", "checked_at": "2026-10-02T08:00:00Z", "baseline": map[string]any{"oldest_utc": "04/05/2021"}}),
		"chat key not exact": map[string]any{"account": valid["account"], "chats": map[string]any{"628222000222:3@s.whatsapp.net": valid["chats"].(map[string]any)[verificationChat]}},
	}
	for name, manifest := range cases {
		t.Run(name, func(t *testing.T) {
			writeVerificationManifest(t, manifest)
			resp := chat.HistoryResponse{Completeness: "unknown"}
			applyPhoneVerification(&resp, verificationAccount, verificationChat)
			require.Equal(t, chat.HistoryResponse{Completeness: "unknown"}, resp)
		})
	}

	t.Run("env unset", func(t *testing.T) {
		t.Setenv(phoneVerificationFileEnv, "")
		resp := chat.HistoryResponse{Completeness: "unknown"}
		applyPhoneVerification(&resp, verificationAccount, verificationChat)
		require.Nil(t, resp.PhoneVerification)
	})

	t.Run("missing file", func(t *testing.T) {
		t.Setenv(phoneVerificationFileEnv, filepath.Join(t.TempDir(), "absent.json"))
		resp := chat.HistoryResponse{Completeness: "unknown"}
		applyPhoneVerification(&resp, verificationAccount, verificationChat)
		require.Nil(t, resp.PhoneVerification)
	})

	t.Run("nil response and blank chat", func(t *testing.T) {
		writeVerificationManifest(t, valid)
		applyPhoneVerification(nil, verificationAccount, verificationChat)
		resp := chat.HistoryResponse{}
		applyPhoneVerification(&resp, verificationAccount, "  ")
		require.Nil(t, resp.PhoneVerification)
	})
}

func TestParsePhoneTimestamp(t *testing.T) {
	for _, value := range []string{"2021-05-04T10:00:00Z", " 2021-05-04T10:00:00.123+07:00 ", "2021-05-04 10:00:00+00:00"} {
		_, err := parsePhoneTimestamp(value)
		require.NoError(t, err, value)
	}
	for _, value := range []string{"", "2021-05-04", "2021-05-04 10:00:00", "04/05/2021"} {
		parsed, err := parsePhoneTimestamp(value)
		require.ErrorIs(t, err, os.ErrInvalid, value)
		require.True(t, parsed.IsZero(), value)
	}
}

func TestCanonicalJID(t *testing.T) {
	require.Equal(t, "628111000111@s.whatsapp.net", canonicalJID(" 628111000111:7@s.whatsapp.net "))
	require.Equal(t, "123456789012345@lid", canonicalJID("123456789012345@lid"))
	require.Empty(t, canonicalJID(""))
	require.Empty(t, canonicalJID("628111000111:x@s.whatsapp.net"))
}
