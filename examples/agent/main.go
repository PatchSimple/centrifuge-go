package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge-go"
)

// build flags
var (
	Commit = "unknown"
)

const (
	defaultConfigFilename   = "agent_config.json"
	defaultTokenExeFilename = "token.exe"
)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
}

func main() {
	slog.Info("starting",
		"commit", Commit,
		"pid", os.Getpid(),
		"wd", mustGetWd(),
	)
	go servePprof()
	// Gracefully handle shutdown signals.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, os.Kill)
	defer stop()
	// Run the agent forever.
	if err := runAgent(ctx); err != nil {
		slog.Error("failed to run agent", "reason", err)
		os.Exit(1)
	}
}

func servePprof() {
	// pprof mutates the default http server.
	if err := http.ListenAndServe("localhost:6060", nil); !errors.Is(err, http.ErrServerClosed) {
		slog.Error("failed to serve pprof", "reason", err)
	}
}

func runAgent(ctx context.Context) error {
	// Load config.
	cfg, err := getConfig()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	// Build connector.
	connector := newCentrifugeClientConnector(
		cfg.ConnURL,
		cfg.Channel,
		cfg.Message,
		centrifuge.Config{
			// Use the token from the external executable.
			GetToken: newGetTokenFunc(cfg.TokenExePath),
		},
	)
	// Run forever.
	if err := runGroupFaultTolerant(ctx, connector.run); err != nil {
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("failed to run connector: %w", err)
		}
	}
	return nil
}

type config struct {
	TokenExePath string `json:"tokenExePath"`
	ConnURL      string `json:"connUrl"`
	Channel      string `json:"channel"`
	Message      string `json:"message"`
}

func getConfig() (config, error) {
	dir, err := os.Getwd()
	if err != nil {
		return config{}, fmt.Errorf("failed to get working directory: %w", err)
	}
	configPath := filepath.Join(dir, defaultConfigFilename)
	file, err := os.Open(configPath)
	if err != nil {
		return config{}, fmt.Errorf("failed to open config file: %w", err)
	}
	defer file.Close()
	b, err := io.ReadAll(io.LimitReader(file, 1<<20)) // Limit to 1MB
	if err != nil {
		return config{}, fmt.Errorf("failed to read config file: %w", err)
	}
	slog.Debug("read config file", "path", configPath, "content", string(b))
	var cfg config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return config{}, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	if cfg.ConnURL == "" {
		return config{}, errors.New("connURL is required")
	}
	if cfg.Channel == "" {
		return config{}, errors.New("channel is required")
	}
	if cfg.Message == "" {
		return config{}, errors.New("message is required")
	}
	if cfg.TokenExePath == "" {
		cfg.TokenExePath = filepath.Join(dir, defaultTokenExeFilename)
	}
	return cfg, nil
}

// newGetTokenFunc gets the auth token needed to communicate with centrifuge.
// This is normally internal auth logic. as a work around, this function calls
// an external executable to get the token to avoid exposing the authentication
// process in this binary.
func newGetTokenFunc(execPath string) func(_ centrifuge.ConnectionTokenEvent) (string, error) {
	return func(_ centrifuge.ConnectionTokenEvent) (string, error) {
		slog.Debug("getToken was called")
		token, err := runTokenExec(execPath)
		if err != nil {
			return "", fmt.Errorf("failed to get token: %w", err)
		}
		slog.Debug("getToken succeed in freshing its token")
		return token, nil
	}
}

func runTokenExec(execPath string) (string, error) {
	slog.Debug("getToken is executing external token executable", "path", execPath)
	cmd := exec.Command(execPath)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to execute command: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

const (
	codeDisconnectCalled = 0
)

type CentrifugeClientConnector struct {
	connURL            string
	config             centrifuge.Config
	channel            string
	message            string
	publishInterval    time.Duration
	stateCheckInterval time.Duration
}

func newCentrifugeClientConnector(connURL string, channel string, msg string, config centrifuge.Config) *CentrifugeClientConnector {
	return &CentrifugeClientConnector{
		connURL:            connURL,
		config:             config,
		channel:            channel,
		message:            msg,
		publishInterval:    time.Minute * 5,
		stateCheckInterval: time.Minute,
	}
}

func (c *CentrifugeClientConnector) newCentrifugeClient() (client *centrifuge.Client, disconnectSignals chan struct{}) {
	client = centrifuge.NewJsonClient(c.connURL, c.config)
	client.OnError(func(ee centrifuge.ErrorEvent) {
		go func() {
			var centrifugeError centrifuge.Error
			if errors.As(ee.Error, &centrifugeError) {
				slog.Error("centrifuge client emitted an ErrorEvent", "reason", ee.Error, "centrifugeError", centrifugeError)
				return
			}
			slog.Error("centrifuge client emitted an ErrorEvent", "reason", ee.Error)
		}()
	})
	client.OnPublication(func(spe centrifuge.ServerPublicationEvent) {
		go func() {
			slog.Debug("the centrifuge client received a publication", "ServerPublicationEvent", spe)
		}()
	})
	disconnectSignals = make(chan struct{}, 1)
	client.OnDisconnected(func(de centrifuge.DisconnectedEvent) {
		go func() {
			slog.Info("the centrifuge client is disconnected", "DisconnectedEvent", de)
			// Do not send a disconnect signal if the disconnect was explicitly called by the client.
			if de.Code == codeDisconnectCalled {
				return
			}
			// Only send a signal if the channel is not full.
			select {
			case disconnectSignals <- struct{}{}:
			default:
			}
		}()
	})
	client.OnConnected(func(ce centrifuge.ConnectedEvent) {
		go func() {
			slog.Info("the centrifuge client connected", "ConnectedEvent", ce)
		}()
	})
	client.OnConnecting(func(ce centrifuge.ConnectingEvent) {
		go func() {
			slog.Info("the centrifuge client is connecting", "ConnectingEvent", ce)
		}()
	})
	return client, disconnectSignals
}

func (c *CentrifugeClientConnector) run(ctx context.Context) error {
	slog.Info("CentrifugeClientConnector is starting to run")
	defer func() {
		slog.Info("CentrifugeClientConnector is shutting down")
	}()
	// Construct the client.
	slog.Debug("CentrifugeClientConnector is building a new centrifuge client")
	client, disconnectSignals := c.newCentrifugeClient()
	// Start connecting.
	slog.Info("CentrifugeClientConnector is starting to connect to server", "url", c.connURL)
	if err := client.Connect(); err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}
	defer client.Close()
	// Build tickers.
	publishTicker := time.NewTicker(c.publishInterval)
	defer publishTicker.Stop()
	stateCheckTicker := time.NewTicker(c.stateCheckInterval)
	defer stateCheckTicker.Stop()
	// Run forever unless the context is canceled, the client is closed, or
	// fails to start connecting again.
	for {
		select {
		// Stop because the context was canceled.
		case <-ctx.Done():
			return ctx.Err()
		// Reconnect the client if it ever disconnects.
		case <-disconnectSignals:
			// Hard check ctx, since Connect does not, and it is possible for ctx
			// and disconnect to happen at the same time.
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			slog.Debug("CentrifugeClientConnector will attempt to reconnect to server", "currentState", client.State())
			if err := client.Connect(); err != nil {
				return fmt.Errorf("failed to connect to server: %w", err)
			}
			slog.Debug("CentrifugeClientConnector started reconnecting", "currentState", client.State())
		// Check the state of the client.
		case <-publishTicker.C:
			slog.Debug("CentrifugeClientConnector is checking the state of the client", "currentState", client.State())
		// Do a publish to check.
		case <-publishTicker.C:
			slog.Debug("CentrifugeClientConnector is attempting to publish message", "currentState", client.State())
			if _, err := client.Publish(ctx, c.channel, []byte(c.message)); err != nil {
				slog.Error("CentrifugeClientConnector failed to publish message", "reason", err, "currentState", client.State())
				if errors.Is(err, centrifuge.ErrClientClosed) {
					return fmt.Errorf("client closed: %w", err)
				}
			}
			slog.Debug("CentrifugeClientConnector succeeded in publishing message")
		}
	}
}

// runGroupFaultTolerant runs the functions in a fault-tolerant group. It will
// restart the group if it stops, unless the parent context is canceled.
func runGroupFaultTolerant(ctx context.Context, fns ...func(ctx context.Context) error) error {
	for {
		switch err := runGroup(ctx, fns...); {
		case context.Cause(ctx) != nil:
			return fmt.Errorf("%w: runGroup was shutdown: %w", ctx.Err(), err)
		case err != nil:
			slog.Error("runGroup encountered an error and will be", "reason", err)
		default:
			slog.Warn("runGroup stopped without error and will be restarted")
		}
	}
}

// runGroup runs all the functions as a group in separate goroutines. It blocks
// while they are running. If one function stops, all functions are signaled to
// stop via the context being canceled.
func runGroup(ctx context.Context, fns ...func(ctx context.Context) error) error {
	errsFn := make([]error, len(fns))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer cancel()
			defer func() {
				if v := recover(); v != nil {
					slog.Error("runGroup fn recovered from a panic", "reason", v)
					errsFn[i] = fmt.Errorf("panic: %v", v)
				}
			}()
			errsFn[i] = fn(ctx)
		}(i)
	}
	wg.Wait()
	return errors.Join(errsFn...)
}

func mustGetWd() string {
	dir, err := os.Getwd()
	if err != nil {
		slog.Error("failed to get current working directory", "reason", err)
	}
	return dir
}
