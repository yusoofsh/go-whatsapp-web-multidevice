package mcpevents

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/stretchr/testify/require"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurabilitySignatureScopeRetryAndRevocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	journal, err := mcpstore.Open(filepath.Join(dir, "journal.db"))
	require.NoError(t, err)
	defer journal.Close()
	active := true
	auth := func(s Subscription) bool { return active && s.Owner == "owner" }
	path := filepath.Join(dir, "state.enc")
	hub, err := Open(path, path+".key", journal, auth)
	require.NoError(t, err)
	now := time.Now()
	hub.now = func() time.Time { return now }
	status := 503
	sent := [][]byte{}
	post := func(_ context.Context, _ string, headers map[string]string, body []byte) (int, []byte, error) {
		mac := hmac.New(sha256.New, bytes.Repeat([]byte("k"), 32))
		mac.Write([]byte(headers["webhook-id"] + "." + headers["webhook-timestamp"] + "."))
		mac.Write(body)
		require.Equal(t, "v1,"+base64.StdEncoding.EncodeToString(mac.Sum(nil)), headers["webhook-signature"])
		var event map[string]any
		require.NoError(t, json.Unmarshal(body, &event))
		if event["type"] == "verification" {
			return 200, body, nil
		}
		sent = append(sent, bytes.Clone(body))
		return status, nil, nil
	}
	hub.Post = post
	secret := "whsec_" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte("k"), 32))
	p := map[string]any{"name": "message.created", "arguments": map[string]string{"device_id": "a", "chat_id": "chat"}, "delivery": map[string]string{"mode": "webhook", "url": "https://callback.example.test/events", "secret": secret}, "cursor": "0"}
	raw, _ := json.Marshal(p)
	a, err := hub.Handle(ctx, "events/subscribe", raw, "owner", "a", "private-auth", "alias")
	require.NoError(t, err)
	b, err := hub.Handle(ctx, "events/subscribe", raw, "owner", "a", "private-auth", "alias")
	require.NoError(t, err)
	require.Equal(t, a.(map[string]any)["id"], b.(map[string]any)["id"])
	sealed, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NotContains(t, string(sealed), secret)
	require.NotContains(t, string(sealed), "private-auth")
	_, err = journal.Append(ctx, mcpstore.Event{DeviceID: "b", Type: "message", ChatJID: "chat", MessageID: "hidden"})
	require.NoError(t, err)
	event, err := journal.Append(ctx, mcpstore.Event{DeviceID: "a", Type: "message", ChatJID: "chat", MessageID: "visible"})
	require.NoError(t, err)
	require.NoError(t, hub.Tick(ctx))
	require.Len(t, sent, 1)
	reopened, err := Open(path, path+".key", journal, auth)
	require.NoError(t, err)
	reopened.Post = post
	reopened.now = hub.now
	now = now.Add(2 * time.Second)
	status = 204
	require.NoError(t, reopened.Tick(ctx))
	require.Len(t, sent, 2)
	require.Equal(t, sent[0], sent[1])
	require.Contains(t, string(sent[0]), time.Unix(event.Timestamp, 0).UTC().Format(time.RFC3339))
	require.NotContains(t, string(sent[0]), "hidden")
	active = false
	require.NoError(t, reopened.Tick(ctx))
	require.Empty(t, reopened.subscriptions)
}
func TestPrivateIPAndWrongChallenge(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.1.1", "::1", "::ffff:127.0.0.1", "fc00::1", "2002:7f00:1::", "2001:db8::1"} {
		require.False(t, publicIP(netip.MustParseAddr(ip)), ip)
	}
	require.True(t, publicIP(netip.MustParseAddr("1.1.1.1")))
	dir := t.TempDir()
	journal, err := mcpstore.Open(filepath.Join(dir, "journal"))
	require.NoError(t, err)
	defer journal.Close()
	hub, err := Open(filepath.Join(dir, "state"), filepath.Join(dir, "key"), journal, func(Subscription) bool { return true })
	require.NoError(t, err)
	hub.Post = func(context.Context, string, map[string]string, []byte) (int, []byte, error) {
		return 200, []byte(`{"challenge":"wrong"}`), nil
	}
	raw := json.RawMessage(`{"name":"message.created","arguments":{"device_id":"a"},"delivery":{"mode":"webhook","url":"https://callback.example.test/events","secret":"whsec_a2tra2tra2tra2tra2tra2tra2tra2tra2tra2tra2s="}}`)
	_, err = hub.Handle(context.Background(), "events/subscribe", raw, "owner", "a", "auth", "alias")
	require.Error(t, err)
	require.Equal(t, -32015, err.(*RPCError).Code)
	require.Empty(t, hub.subscriptions)
}
