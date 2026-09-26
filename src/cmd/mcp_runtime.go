package cmd

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/aldinokemal/go-whatsapp-web-multidevice/config"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/mcpstore"
	"github.com/aldinokemal/go-whatsapp-web-multidevice/infrastructure/whatsapp"
	uimcp "github.com/aldinokemal/go-whatsapp-web-multidevice/ui/mcp"
	mcpoauth "github.com/aldinokemal/go-whatsapp-web-multidevice/ui/mcp/oauth"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
)

var mcpData *mcpstore.Store

func init() {
	flags := rootCmd.PersistentFlags()
	flags.BoolVar(&config.McpStreamingEnabled, "mcp-streaming-enabled", false, "enable native HTTP MCP streaming gateway")
	flags.StringVar(&config.McpStreamPort, "mcp-stream-port", "3001", "native MCP gateway port; forward public traffic here")
	flags.StringVar(&config.McpDataDir, "mcp-data-dir", "storages/mcp", "private MCP data directory outside public statics")
}
func loadMcpRuntimeConfig() {
	flags := rootCmd.PersistentFlags()
	if !flags.Changed("mcp-streaming-enabled") && viper.IsSet("mcp_streaming_enabled") {
		config.McpStreamingEnabled = viper.GetBool("mcp_streaming_enabled")
	}
	if !flags.Changed("mcp-stream-port") && viper.GetString("mcp_stream_port") != "" {
		config.McpStreamPort = viper.GetString("mcp_stream_port")
	}
	if !flags.Changed("mcp-data-dir") && viper.GetString("mcp_data_dir") != "" {
		config.McpDataDir = viper.GetString("mcp_data_dir")
	}
}
func runtimeMcpDeps() uimcp.Deps {
	return uimcp.Deps{Data: mcpData, App: appUsecase, Send: sendUsecase, Chat: chatUsecase, User: userUsecase, Message: messageUsecase, Group: groupUsecase, Device: deviceUsecase, Newsletter: newsletterUsecase, Call: callUsecase, Schedule: scheduleUsecase}
}
func initializeMcpData() error {
	if !config.McpEnabled {
		return nil
	}
	if err := uimcp.ValidatePrivateDataDir(config.McpDataDir, "statics"); err != nil {
		return err
	}
	var err error
	mcpData, err = mcpstore.Open(filepath.Join(config.McpDataDir, "mcp.db"))
	if err == nil {
		whatsapp.SetMCPEventStore(mcpData)
	}
	return err
}
func startNativeMcpGateway(dm *whatsapp.DeviceManager, oauthServer *mcpoauth.Server) (func(), error) {
	noop := func() {}
	if !config.McpEnabled || !config.McpStreamingEnabled {
		return noop, nil
	}
	if config.McpStreamPort == config.AppPort {
		return noop, errors.New("native MCP and Fiber ports must differ")
	}
	basic, err := mcpOAuthCredentialValidator(config.AppBasicAuthCredential)
	if err != nil {
		return noop, err
	}
	basicLimiter := newBasicAuthFailureLimiter()
	authenticate := func(r *http.Request) (string, error) {
		scheme, _, _, _ := parseBasicAuthorization(r.Header.Get("Authorization"))
		if scheme == "basic" {
			username, valid, err := authenticateNativeBasic(r, basic, basicLimiter)
			if err != nil {
				return "", err
			}
			if valid {
				if oauthServer != nil {
					return oauthServer.AuthenticateHTTP(r.Context(), r.Header.Get("Authorization"), basic)
				}
				return "basic:" + username, nil
			}
		}
		if oauthServer != nil {
			return oauthServer.AuthenticateHTTP(r.Context(), r.Header.Get("Authorization"), basic)
		}
		user, password, ok := r.BasicAuth()
		if !ok || !basic(user, password) {
			return "", errors.New("invalid credentials")
		}
		return "basic:" + user, nil
	}
	challenge := `Basic realm="GOWA MCP"`
	if oauthServer != nil {
		challenge = oauthServer.HTTPChallenge()
	}
	origins := append([]string{}, config.AppCORSAllowedOrigins...)
	origins = append(origins, uimcp.PublicOrigin(config.McpOAuthIssuerURL))
	handler := uimcp.NewNativeHandler(runtimeMcpDeps(), dm, authenticate, challenge, origins)
	targetHost := config.AppHost
	if targetHost == "" || targetHost == "0.0.0.0" || targetHost == "::" {
		targetHost = "127.0.0.1"
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(targetHost, config.AppPort)}
	proxy := httputil.NewSingleHostReverseProxy(target)
	route := config.AppBasePath + "/mcp"
	gateway := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" && r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte(`{"status":"ok","transport":"streamable-http"}`))
			return
		}
		if r.URL.Path == route {
			handler.ServeHTTP(w, r)
			return
		}
		// Discovery, OAuth, REST and UI stay in Fiber; the SSE stream never does.
		proxy.ServeHTTP(w, r)
	})
	listener, err := net.Listen("tcp", net.JoinHostPort(config.AppHost, config.McpStreamPort))
	if err != nil {
		_ = handler.Close(context.Background())
		return noop, err
	}
	srv := &http.Server{Handler: gateway, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logrus.Errorf("Native MCP listener: %v", err)
		}
	}()
	logrus.Infof("Native MCP streaming gateway listening on %s", listener.Addr())
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = handler.Close(ctx)
			if srv.Shutdown(ctx) != nil {
				_ = srv.Close()
			}
		})
	}, nil
}
