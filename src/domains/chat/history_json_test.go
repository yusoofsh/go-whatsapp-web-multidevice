package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRequestChatHistoryRequestDecodesAnchorFields(t *testing.T) {
	var request RequestChatHistoryRequest
	body := `{"count":20,"history_jid":"123456789012345@lid","anchor_message_id":"ABC","anchor_timestamp":"2024-01-02T03:04:05Z","anchor_is_from_me":false}`
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatal(err)
	}
	if request.Count != 20 || request.HistoryJID != "123456789012345@lid" || request.AnchorMessageID != "ABC" ||
		request.AnchorTimestamp != "2024-01-02T03:04:05Z" || request.AnchorIsFromMe == nil || *request.AnchorIsFromMe {
		t.Fatalf("unexpected decode %+v", request)
	}
}

func TestHistoryResponseOmitsUnsetPhoneVerification(t *testing.T) {
	raw, err := json.Marshal(HistoryResponse{Completeness: "unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "phone_verification") {
		t.Fatalf("phone_verification must be omitted when unset: %s", raw)
	}

	raw, err = json.Marshal(HistoryResponse{PhoneVerification: &PhoneVerification{Status: "retention_only", Scope: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"phone_verification":{"status":"retention_only","scope":"s"}`) {
		t.Fatalf("unexpected encoding: %s", raw)
	}
}
