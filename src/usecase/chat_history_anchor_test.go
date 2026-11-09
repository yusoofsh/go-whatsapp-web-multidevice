package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainChat "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chat"
	domainChatStorage "github.com/aldinokemal/go-whatsapp-web-multidevice/domains/chatstorage"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	pkgError "github.com/aldinokemal/go-whatsapp-web-multidevice/pkg/error"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

type lidStoreFake struct {
	store.LIDStore
	pn     types.JID
	err    error
	called bool
}

func (f *lidStoreFake) GetPNForLID(_ context.Context, _ types.JID) (types.JID, error) {
	f.called = true
	return f.pn, f.err
}

type anchorRepoStub struct {
	chatUsecaseRepoStub
	byID       *domainChatStorage.Message
	byIDErr    error
	byIDCalled bool
	byIDArgs   [3]string
}

func (r *anchorRepoStub) GetMessageByIDChatAndDevice(deviceID, chatJID, id string) (*domainChatStorage.Message, error) {
	r.byIDCalled = true
	r.byIDArgs = [3]string{deviceID, chatJID, id}
	return r.byID, r.byIDErr
}

func requireValidationError(t *testing.T, err error, want string) {
	t.Helper()
	var validationErr pkgError.ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ValidationError containing %q, got %T: %v", want, err, err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), want)
	}
}

func TestResolveHistoryJID(t *testing.T) {
	chatJID := types.NewJID("628123456789", types.DefaultUserServer)
	lid := "123456789012345@lid"
	clientWith := func(fake *lidStoreFake) *whatsmeow.Client {
		return &whatsmeow.Client{Store: &store.Device{LIDs: fake}}
	}

	t.Run("invalid JID", func(t *testing.T) {
		_, err := resolveHistoryJID(context.Background(), nil, chatJID, "628123456789:x@s.whatsapp.net")
		requireValidationError(t, err, "history_jid must be a valid JID")
	})

	t.Run("same chat in device form returns the chat", func(t *testing.T) {
		fake := &lidStoreFake{}
		got, err := resolveHistoryJID(context.Background(), clientWith(fake), chatJID, "628123456789:3@s.whatsapp.net")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != chatJID {
			t.Fatalf("got %s, want %s", got, chatJID)
		}
		if fake.called {
			t.Fatal("LID store must not be consulted for the chat itself")
		}
	})

	t.Run("other phone number is rejected", func(t *testing.T) {
		_, err := resolveHistoryJID(context.Background(), clientWith(&lidStoreFake{}), chatJID, "628000000000@s.whatsapp.net")
		requireValidationError(t, err, "must match the canonical chat or a mapped LID")
	})

	t.Run("LID without a LID store is rejected", func(t *testing.T) {
		for name, client := range map[string]*whatsmeow.Client{
			"nil client": nil,
			"nil store":  {},
			"nil LIDs":   {Store: &store.Device{}},
		} {
			_, err := resolveHistoryJID(context.Background(), client, chatJID, lid)
			if err == nil {
				t.Fatalf("%s: expected an error", name)
			}
			requireValidationError(t, err, "must match the canonical chat or a mapped LID")
		}
	})

	t.Run("LID mapped to the chat is accepted", func(t *testing.T) {
		fake := &lidStoreFake{pn: types.NewADJID("628123456789", 0, 5)}
		got, err := resolveHistoryJID(context.Background(), clientWith(fake), chatJID, lid)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.String() != lid {
			t.Fatalf("got %s, want %s", got, lid)
		}
	})

	t.Run("LID mapped elsewhere is rejected", func(t *testing.T) {
		fake := &lidStoreFake{pn: types.NewJID("628000000000", types.DefaultUserServer)}
		_, err := resolveHistoryJID(context.Background(), clientWith(fake), chatJID, lid)
		requireValidationError(t, err, "LID is not mapped to the requested chat")
	})

	t.Run("unmapped LID is rejected", func(t *testing.T) {
		fake := &lidStoreFake{pn: types.EmptyJID}
		_, err := resolveHistoryJID(context.Background(), clientWith(fake), chatJID, lid)
		requireValidationError(t, err, "LID is not mapped to the requested chat")
	})

	t.Run("LID store error is rejected", func(t *testing.T) {
		fake := &lidStoreFake{err: errors.New("db closed")}
		_, err := resolveHistoryJID(context.Background(), clientWith(fake), chatJID, lid)
		requireValidationError(t, err, "LID is not mapped to the requested chat")
	})
}

func TestResolveHistoryAnchorExplicit(t *testing.T) {
	const deviceID = "628999999999@s.whatsapp.net"
	const chatJID = "628123456789@s.whatsapp.net"
	fromMe := true

	t.Run("full triple builds an anchor without storage", func(t *testing.T) {
		repo := &anchorRepoStub{}
		got, err := resolveHistoryAnchor(repo, deviceID, domainChat.RequestChatHistoryRequest{
			ChatJID:         chatJID,
			AnchorMessageID: "ABC",
			AnchorTimestamp: "2024-01-02T03:04:05+07:00",
			AnchorIsFromMe:  &fromMe,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2024, time.January, 1, 20, 4, 5, 0, time.UTC)
		if got.ID != "ABC" || got.ChatJID != chatJID || got.DeviceID != deviceID || !got.IsFromMe || !got.Timestamp.Equal(want) {
			t.Fatalf("unexpected anchor %+v", got)
		}
		if repo.byIDCalled || repo.oldestMessageCalled {
			t.Fatal("an explicit anchor must not read storage")
		}
	})

	cases := []struct {
		name    string
		request domainChat.RequestChatHistoryRequest
		want    string
	}{
		{"timestamp without the rest", domainChat.RequestChatHistoryRequest{AnchorTimestamp: "2024-01-02T03:04:05Z"}, "must be provided together"},
		{"from_me without the rest", domainChat.RequestChatHistoryRequest{AnchorIsFromMe: &fromMe}, "must be provided together"},
		{"missing from_me", domainChat.RequestChatHistoryRequest{AnchorMessageID: "ABC", AnchorTimestamp: "2024-01-02T03:04:05Z"}, "must be provided together"},
		{"ID longer than 256", domainChat.RequestChatHistoryRequest{AnchorMessageID: strings.Repeat("a", 257), AnchorTimestamp: "2024-01-02T03:04:05Z", AnchorIsFromMe: &fromMe}, "must be provided together"},
		{"not RFC3339", domainChat.RequestChatHistoryRequest{AnchorMessageID: "ABC", AnchorTimestamp: "2024-01-02 03:04:05", AnchorIsFromMe: &fromMe}, "positive RFC3339"},
		{"epoch", domainChat.RequestChatHistoryRequest{AnchorMessageID: "ABC", AnchorTimestamp: "1970-01-01T00:00:00Z", AnchorIsFromMe: &fromMe}, "positive RFC3339"},
		{"before epoch", domainChat.RequestChatHistoryRequest{AnchorMessageID: "ABC", AnchorTimestamp: "1969-12-31T23:59:59Z", AnchorIsFromMe: &fromMe}, "positive RFC3339"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.request.ChatJID = chatJID
			repo := &anchorRepoStub{}
			_, err := resolveHistoryAnchor(repo, deviceID, tc.request)
			requireValidationError(t, err, tc.want)
			if repo.byIDCalled || repo.oldestMessageCalled {
				t.Fatal("a rejected explicit anchor must not read storage")
			}
		})
	}

	t.Run("ID of exactly 256 is accepted", func(t *testing.T) {
		_, err := resolveHistoryAnchor(&anchorRepoStub{}, deviceID, domainChat.RequestChatHistoryRequest{
			ChatJID:         chatJID,
			AnchorMessageID: strings.Repeat("a", 256),
			AnchorTimestamp: "2024-01-02T03:04:05Z",
			AnchorIsFromMe:  &fromMe,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestResolveHistoryAnchorStored(t *testing.T) {
	const deviceID = "628999999999@s.whatsapp.net"
	const chatJID = "628123456789@s.whatsapp.net"
	stored := func(mutate func(*domainChatStorage.Message)) *domainChatStorage.Message {
		message := &domainChatStorage.Message{ID: "M1", ChatJID: chatJID, DeviceID: deviceID, Timestamp: time.Unix(1700000000, 0)}
		if mutate != nil {
			mutate(message)
		}
		return message
	}

	t.Run("named message is looked up by full identity", func(t *testing.T) {
		repo := &anchorRepoStub{byID: stored(nil)}
		got, err := resolveHistoryAnchor(repo, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID, AnchorMessageID: "M1"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != repo.byID {
			t.Fatalf("got %+v, want the stored message", got)
		}
		if repo.byIDArgs != [3]string{deviceID, chatJID, "M1"} {
			t.Fatalf("lookup args = %v", repo.byIDArgs)
		}
		if repo.oldestMessageCalled {
			t.Fatal("a named anchor must not fall back to the oldest message")
		}
	})

	t.Run("no ID uses the oldest stored message", func(t *testing.T) {
		repo := &anchorRepoStub{chatUsecaseRepoStub: chatUsecaseRepoStub{oldestMessage: stored(nil)}}
		got, err := resolveHistoryAnchor(repo, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != "M1" || repo.byIDCalled || !repo.oldestMessageCalled {
			t.Fatalf("got %+v byID=%v oldest=%v", got, repo.byIDCalled, repo.oldestMessageCalled)
		}
	})

	t.Run("named message not found", func(t *testing.T) {
		_, err := resolveHistoryAnchor(&anchorRepoStub{}, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID, AnchorMessageID: "M1"})
		requireValidationError(t, err, "anchor message not found")
	})

	t.Run("no stored messages", func(t *testing.T) {
		_, err := resolveHistoryAnchor(&anchorRepoStub{}, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID})
		requireValidationError(t, err, "chat has no stored messages yet")
	})

	t.Run("storage error is returned as is", func(t *testing.T) {
		lookupErr := errors.New("disk I/O error")
		_, err := resolveHistoryAnchor(&anchorRepoStub{byIDErr: lookupErr}, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID, AnchorMessageID: "M1"})
		if !errors.Is(err, lookupErr) {
			t.Fatalf("got %v, want %v", err, lookupErr)
		}
	})

	for name, mutate := range map[string]func(*domainChatStorage.Message){
		"empty ID":     func(m *domainChatStorage.Message) { m.ID = "" },
		"other chat":   func(m *domainChatStorage.Message) { m.ChatJID = "628000000000@s.whatsapp.net" },
		"other device": func(m *domainChatStorage.Message) { m.DeviceID = "628111111111@s.whatsapp.net" },
		"call entry":   func(m *domainChatStorage.Message) { m.MediaType = "call" },
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			repo := &anchorRepoStub{byID: stored(mutate)}
			_, err := resolveHistoryAnchor(repo, deviceID, domainChat.RequestChatHistoryRequest{ChatJID: chatJID, AnchorMessageID: "M1"})
			requireValidationError(t, err, "anchor message is invalid")
		})
	}
}

// TestRequestChatHistoryAnchorResolvedBeforeLogin pins the order: a bad
// explicit anchor fails before the connection check, so it reports the anchor
// problem even while the client is offline.
func TestRequestChatHistoryAnchorResolvedBeforeLogin(t *testing.T) {
	accountJID := types.NewJID("628999999999", types.DefaultUserServer)
	client := &whatsmeow.Client{Store: &store.Device{ID: &accountJID}}
	repo := &chatUsecaseRepoStub{}
	service := NewChatService(repo)
	ctx := whatsapp.ContextWithDevice(context.Background(), whatsapp.NewDeviceInstance(accountJID.String(), client, nil))

	_, err := service.RequestChatHistory(ctx, domainChat.RequestChatHistoryRequest{
		ChatJID:         "628123456789@s.whatsapp.net",
		Count:           50,
		AnchorTimestamp: "2024-01-02T03:04:05Z",
	})
	requireValidationError(t, err, "must be provided together")
	if repo.oldestMessageCalled {
		t.Fatal("a partial explicit anchor must not fall back to storage")
	}
}
