package mcp

import (
	"context"
	"testing"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/validations"
	"github.com/stretchr/testify/require"
)

func TestStagedMediaPassesProductionValidators(t *testing.T) {
	data := parityStore(t)
	device := pairedTestDevice("a", "111")
	ctx := whatsapp.ContextWithDevice(context.Background(), device)
	spy := &stubSendService{}
	handler := InitMcpSend(spy, &stubResolver{inst: device}, data)
	cases := []struct {
		kind, mime, name string
		body             []byte
	}{
		{"image", "image/png", "x.png", []byte("\x89PNG\r\n\x1a\nfixture")},
		{"video", "video/mp4", "x.mp4", []byte("fixture-video")},
		{"audio", "audio/ogg", "x.ogg", []byte("OggSfixture")},
		{"document", "application/pdf", "x.pdf", []byte("%PDF-1.7\nfixture")},
		{"sticker", "image/png", "x.png", []byte("\x89PNG\r\n\x1a\nfixture")},
	}
	for _, tc := range cases {
		media, err := data.Stage(ctx, device.JID(), tc.name, tc.mime, tc.body)
		require.NoError(t, err)
		result, err := handler.handleSend(ctx, callReq(map[string]any{"type": tc.kind, "phone": "628123456789", "media_id": media.ID}))
		require.NoError(t, err)
		require.False(t, result.IsError)
		switch tc.kind {
		case "image":
			require.Nil(t, spy.lastImage.ImageURL)
			require.NoError(t, validations.ValidateSendImage(ctx, *spy.lastImage))
		case "video":
			require.Nil(t, spy.lastVideo.VideoURL)
			require.NoError(t, validations.ValidateSendVideo(ctx, *spy.lastVideo))
		case "audio":
			require.Nil(t, spy.lastAudio.AudioURL)
			require.NoError(t, validations.ValidateSendAudio(ctx, *spy.lastAudio))
		case "document":
			require.Nil(t, spy.lastFile.FileURL)
			require.NoError(t, validations.ValidateSendFile(ctx, *spy.lastFile))
		case "sticker":
			require.Nil(t, spy.lastSticker.StickerURL)
			require.NoError(t, validations.ValidateSendSticker(ctx, *spy.lastSticker))
		}
	}
}
