package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/magpern/lts-slack-local-mcp/pkg/auth"
	"github.com/magpern/lts-slack-local-mcp/pkg/provider"
	"github.com/magpern/lts-slack-local-mcp/pkg/server"
	"github.com/mattn/go-isatty"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var defaultSseHost = "127.0.0.1"
var defaultSsePort = 13080

func main() {
	var transport string
	var enabledToolsFlag string
	var noCache bool
	flag.StringVar(&transport, "t", "stdio", "Transport type (stdio, sse or http)")
	flag.StringVar(&transport, "transport", "stdio", "Transport type (stdio, sse or http)")
	flag.StringVar(&enabledToolsFlag, "e", "", "Comma-separated list of enabled tools (empty = all tools)")
	flag.StringVar(&enabledToolsFlag, "enabled-tools", "", "Comma-separated list of enabled tools (empty = all tools)")
	flag.BoolVar(&noCache, "no-cache", false, "Skip user/channel cache loading on startup for faster initialization. Lookups by #channel-name or @username will not work; use channel/user IDs instead.")
	flag.Parse()

	if enabledToolsFlag == "" {
		enabledToolsFlag = os.Getenv("SLACK_MCP_ENABLED_TOOLS")
	}

	var enabledTools []string
	if enabledToolsFlag != "" {
		for _, tool := range strings.Split(enabledToolsFlag, ",") {
			tool = strings.TrimSpace(tool)
			if tool != "" {
				enabledTools = append(enabledTools, tool)
			}
		}
	}

	logger, err := newLogger(transport)
	if err != nil {
		panic(err)
	}
	defer logger.Sync()

	addMessageToolEnv := os.Getenv("SLACK_MCP_ADD_MESSAGE_TOOL")
	err = validateToolConfig(addMessageToolEnv)
	if err != nil {
		logger.Fatal("error in SLACK_MCP_ADD_MESSAGE_TOOL",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}

	err = server.ValidateEnabledTools(enabledTools)
	if err != nil {
		logger.Fatal("error in SLACK_MCP_ENABLED_TOOLS",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}

	p, err := newProvider(transport, logger)
	if err != nil {
		logger.Fatal("Failed to initialise session state",
			zap.String("context", "console"),
			zap.Error(err),
		)
	}
	s := server.NewMCPServer(p, logger, enabledTools)

	if noCache {
		p.SkipCache()
		logger.Info("Cache loading disabled via --no-cache flag",
			zap.String("context", "console"),
		)
	}

	// Reconnect in the background if a previous login exists. This is headless
	// only and never opens a window; failure is not fatal because the server
	// must stay usable (login/logout) while signed out.
	if p.Session().SignedOut() {
		logger.Info("Signed out; call the login tool to sign in",
			zap.String("context", "console"))
	} else if p.Session().Pin() != nil || hasEnvCredential() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := p.Connect(ctx); err != nil {
				logger.Warn("Not connected to Slack yet; call the login tool",
					zap.String("context", "console"),
					zap.Error(err))
			}
		}()
	} else {
		logger.Info("No Slack account linked yet; call the login tool to sign in",
			zap.String("context", "console"))
	}

	switch transport {
	case "stdio":
		if err := s.ServeStdio(); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	case "sse":
		host := os.Getenv("SLACK_MCP_HOST")
		if host == "" {
			host = defaultSseHost
		}
		port := os.Getenv("SLACK_MCP_PORT")
		if port == "" {
			port = strconv.Itoa(defaultSsePort)
		}

		sseServer := s.ServeSSE(":" + port)
		logger.Info(
			fmt.Sprintf("SSE server listening on %s", fmt.Sprintf("%s:%s/sse", host, port)),
			zap.String("context", "console"),
			zap.String("host", host),
			zap.String("port", port),
		)

		if ready, _ := p.IsReady(); !ready {
			logger.Info("Slack MCP Server is still warming up caches",
				zap.String("context", "console"),
			)
		}

		if err := sseServer.Start(host + ":" + port); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	case "http":
		host := os.Getenv("SLACK_MCP_HOST")
		if host == "" {
			host = defaultSseHost
		}
		port := os.Getenv("SLACK_MCP_PORT")
		if port == "" {
			port = strconv.Itoa(defaultSsePort)
		}

		httpServer := s.ServeHTTP(":" + port)
		logger.Info(
			fmt.Sprintf("HTTP server listening on %s", fmt.Sprintf("%s:%s", host, port)),
			zap.String("context", "console"),
			zap.String("host", host),
			zap.String("port", port),
		)

		if ready, _ := p.IsReady(); !ready {
			logger.Info("Slack MCP Server is still warming up caches",
				zap.String("context", "console"),
			)
		}

		if err := httpServer.Start(host + ":" + port); err != nil {
			logger.Fatal("Server error",
				zap.String("context", "console"),
				zap.Error(err),
			)
		}
	default:
		logger.Fatal("Invalid transport type",
			zap.String("context", "console"),
			zap.String("transport", transport),
			zap.String("allowed", "stdio, sse, http"),
		)
	}
}

// newProvider wires the session lifecycle: environment credentials (if set) are
// an optional static override; otherwise the dedicated browser profile is used.
func newProvider(transport string, logger *zap.Logger) (*provider.ApiProvider, error) {
	cfg := auth.ConfigFromEnv()
	store, err := auth.OpenStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	var harv auth.Harvester = auth.NewRodHarvester(cfg, logger)
	envCred, haveEnv := auth.EnvCredential()
	if haveEnv {
		harv = auth.StaticHarvester{Cred: envCred}
	}
	sess := auth.NewSessionAuth(cfg, store, harv, auth.NewSlackValidator(logger), logger)
	if haveEnv {
		sess.AllowAutoFirstLogin() // static credentials: no browser, safe to connect unattended
	}
	return provider.NewLocal(transport, logger, sess), nil
}

func hasEnvCredential() bool {
	_, ok := auth.EnvCredential()
	return ok
}

func validateToolConfig(config string) error {
	if config == "" || config == "true" || config == "1" {
		return nil
	}

	items := strings.Split(config, ",")
	hasNegated := false
	hasPositive := false

	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "!") {
			hasNegated = true
		} else {
			hasPositive = true
		}
	}

	if hasNegated && hasPositive {
		return fmt.Errorf("cannot mix allowed and disallowed (! prefixed) channels")
	}

	return nil
}

func newLogger(transport string) (*zap.Logger, error) {
	atomicLevel := zap.NewAtomicLevelAt(zap.InfoLevel)
	if envLevel := os.Getenv("SLACK_MCP_LOG_LEVEL"); envLevel != "" {
		if err := atomicLevel.UnmarshalText([]byte(envLevel)); err != nil {
			// stderr only: stdout carries the stdio JSON-RPC protocol stream.
			fmt.Fprintf(os.Stderr, "Invalid log level '%s': %v, using 'info'\n", envLevel, err)
		}
	}

	useJSON := shouldUseJSONFormat()
	useColors := shouldUseColors() && !useJSON

	outputPath := "stdout"
	if transport == "stdio" {
		outputPath = "stderr"
	}

	var config zap.Config

	if useJSON {
		config = zap.Config{
			Level:            atomicLevel,
			Development:      false,
			Encoding:         "json",
			OutputPaths:      []string{outputPath},
			ErrorOutputPaths: []string{"stderr"},
			EncoderConfig: zapcore.EncoderConfig{
				TimeKey:       "timestamp",
				LevelKey:      "level",
				NameKey:       "logger",
				MessageKey:    "message",
				StacktraceKey: "stacktrace",
				EncodeLevel:   zapcore.LowercaseLevelEncoder,
				EncodeTime:    zapcore.RFC3339TimeEncoder,
				EncodeCaller:  zapcore.ShortCallerEncoder,
			},
		}
	} else {
		config = zap.Config{
			Level:            atomicLevel,
			Development:      true,
			Encoding:         "console",
			OutputPaths:      []string{outputPath},
			ErrorOutputPaths: []string{"stderr"},
			EncoderConfig: zapcore.EncoderConfig{
				TimeKey:          "timestamp",
				LevelKey:         "level",
				NameKey:          "logger",
				MessageKey:       "msg",
				StacktraceKey:    "stacktrace",
				EncodeLevel:      getConsoleLevelEncoder(useColors),
				EncodeTime:       zapcore.ISO8601TimeEncoder,
				EncodeCaller:     zapcore.ShortCallerEncoder,
				ConsoleSeparator: " | ",
			},
		}
	}

	logger, err := config.Build(zap.AddCaller())
	if err != nil {
		return nil, err
	}

	logger = logger.With(zap.String("app", "slack-mcp-server"))

	return logger, err
}

// shouldUseJSONFormat determines if JSON format should be used
func shouldUseJSONFormat() bool {
	if format := os.Getenv("SLACK_MCP_LOG_FORMAT"); format != "" {
		return strings.ToLower(format) == "json"
	}

	if env := os.Getenv("ENVIRONMENT"); env != "" {
		switch strings.ToLower(env) {
		case "production", "prod", "staging":
			return true
		case "development", "dev", "local":
			return false
		}
	}

	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" ||
		os.Getenv("DOCKER_CONTAINER") != "" ||
		os.Getenv("container") != "" {
		return true
	}

	if !isatty.IsTerminal(os.Stdout.Fd()) {
		return true
	}

	return false
}

func shouldUseColors() bool {
	if colorEnv := os.Getenv("SLACK_MCP_LOG_COLOR"); colorEnv != "" {
		return colorEnv == "true" || colorEnv == "1"
	}

	if os.Getenv("NO_COLOR") != "" {
		return false
	}

	if os.Getenv("FORCE_COLOR") != "" {
		return true
	}

	if env := os.Getenv("ENVIRONMENT"); env == "development" || env == "dev" {
		return isatty.IsTerminal(os.Stdout.Fd())
	}

	return isatty.IsTerminal(os.Stdout.Fd())
}

func getConsoleLevelEncoder(useColors bool) zapcore.LevelEncoder {
	if useColors {
		return zapcore.CapitalColorLevelEncoder
	}
	return zapcore.CapitalLevelEncoder
}
