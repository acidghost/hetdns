package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/acidghost/hetdns/internal/app"
	"github.com/acidghost/hetdns/internal/buildinfo"
	"github.com/acidghost/hetdns/internal/config"
)

var (
	buildVersion string
	buildCommit  string
	buildDate    string
)

func main() {
	if code := run(os.Args[1:], os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func run(arguments []string, stdout, stderr io.Writer) int {
	build := buildinfo.New(buildVersion, buildCommit, buildDate)
	flags := flag.NewFlagSet("hetdns", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String(
		"config",
		envOr("HETDNS_CONFIG", "/etc/hetdns/config.json"),
		"JSON configuration path",
	)
	tokenFile := flags.String(
		"token-file",
		os.Getenv("HETDNS_HETZNER_TOKEN_FILE"),
		"file containing the Hetzner token",
	)
	interval := flags.Duration(
		"interval",
		5*time.Minute,
		"reconciliation interval",
	)
	listen := flags.String(
		"listen",
		envOr("HETDNS_LISTEN", ":8080"),
		"HTTP listen address",
	)
	logLevel := flags.String(
		"log-level",
		envOr("HETDNS_LOG_LEVEL", "info"),
		"debug, info, warn, or error",
	)
	logFormat := flags.String(
		"log-format",
		envOr("HETDNS_LOG_FORMAT", "json"),
		"json or text",
	)
	historyLimit := flags.Int("history-limit", 100, "maximum in-memory event count")
	check := flags.Bool("check", false, "validate configuration and secret, then exit")
	version := flags.Bool("version", false, "print build information and exit")
	flags.Usage = func() {
		const usage = "Usage: hetdns [flags]\n\n" +
			"Read-only Hetzner Cloud dynamic DNS reconciler.\n\n" +
			"Flags:\n"

		fmt.Fprint(flags.Output(), usage)
		flags.PrintDefaults()
	}

	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "hetdns does not accept positional arguments")
		return 2
	}

	explicit := make(map[string]bool)
	flags.Visit(func(value *flag.Flag) { explicit[value.Name] = true })
	if !explicit["interval"] {
		value, err := environmentDuration("HETDNS_INTERVAL", *interval)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		*interval = value
	}

	if !explicit["history-limit"] {
		value, err := environmentInt("HETDNS_HISTORY_LIMIT", *historyLimit)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		*historyLimit = value
	}

	if *version {
		fmt.Fprintf(
			stdout,
			"hetdns %s\ncommit: %s\nbuilt: %s\n",
			build.Version,
			build.Commit,
			build.Date,
		)
		return 0
	}
	if *interval < time.Minute {
		fmt.Fprintln(stderr, "interval must be at least 1m")
		return 2
	}
	if *historyLimit < 1 || *historyLimit > 10000 {
		fmt.Fprintln(stderr, "history-limit must be between 1 and 10000")
		return 2
	}

	level, err := parseLogLevel(*logLevel)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if *logFormat != "json" && *logFormat != "text" {
		fmt.Fprintln(stderr, "log-format must be json or text")
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "configuration error: %v\n", err)
		return 2
	}
	token, err := config.ReadToken(*tokenFile, os.Getenv("HETDNS_HETZNER_TOKEN"))
	if err != nil {
		fmt.Fprintf(stderr, "secret error: %v\n", err)
		return 2
	}
	if *check {
		fmt.Fprintln(stdout, "configuration is valid")
		return 0
	}

	var handler slog.Handler
	handlerOptions := &slog.HandlerOptions{Level: level}
	if *logFormat == "text" {
		handler = slog.NewTextHandler(stderr, handlerOptions)
	} else {
		handler = slog.NewJSONHandler(stderr, handlerOptions)
	}
	logger := slog.New(handler)
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	options := app.Options{
		Config:       cfg,
		Token:        token,
		Interval:     *interval,
		Listen:       *listen,
		HistoryLimit: *historyLimit,
		Build:        build,
		Logger:       logger,
	}
	if err := app.Run(ctx, options); err != nil {
		logger.Error("service failed", "component", "main", "error", err)
		return 1
	}

	return 0
}

func envOr(key, fallback string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return fallback
}

func environmentDuration(key string, fallback time.Duration) (time.Duration, error) {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a duration: %w", key, err)
	}
	return parsed, nil
}

func environmentInt(key string, fallback int) (int, error) {
	value, exists := os.LookupEnv(key)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}

func parseLogLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, errors.New("log-level must be debug, info, warn, or error")
	}
}
