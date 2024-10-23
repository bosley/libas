package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	libascli "github.com/bosley/libas/client"
	"github.com/bosley/libas/scribe"
	libaserv "github.com/bosley/libas/server"
)

var (
	verbose bool
	token   string
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	clientList := libaserv.NewClientList()

	serverAddr := flag.String("server", "", "Server address (host:port)")
	playFile := flag.String("play", "", "Play audio file")
	insecureMode := flag.Bool("insecure", false, "Enable insecure mode (skip certificate verification)")
	serverCertFile := flag.String("cert", "", "Path to server certificate file")
	serverKeyFile := flag.String("key", "", "Path to server key file")
	whisperPath := flag.String("whisper", "", "Path to whisper executable (required for server mode)")
	whisperModel := flag.String("model", "", "Path to whisper model file (required for server mode)")
	listDevices := flag.Bool("list-devices", false, "List available audio input devices")
	deviceID := flag.Int("device", 0, "Audio input device ID to use")
	flag.Parse()

	if *playFile != "" {
		err := libascli.PlayAudioFile(*playFile)
		if err != nil {
			slog.Error("Failed to play audio file", "error", err)
		}
		return
	}

	if *listDevices {
		devices, err := libascli.ListAudioDevices()
		if err != nil {
			slog.Error("Failed to list audio devices", "error", err)
			os.Exit(1)
		}

		fmt.Println("Available audio input devices:")
		for i, device := range devices {
			fmt.Printf("[%d] %s\n", i, device.Name)
			fmt.Printf("    Max Input Channels: %d\n", device.MaxInputChannels)
			fmt.Printf("    Default Sample Rate: %f\n", device.DefaultSampleRate)
			fmt.Println()
		}
		return
	}

	token = os.Getenv("LIBAS_TOKEN")
	if token == "" {
		slog.Error("LIBAS_TOKEN environment variable is not set")
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		slog.Debug("Received shutdown signal")
		cancel()
	}()

	if *serverAddr == "" {
		if *insecureMode {
			slog.Warn("Running server in insecure mode. This should not be used in production!")
			slog.Error("Non-TLS server mode not implemented yet")
			return
		}
		if *serverCertFile == "" || *serverKeyFile == "" {
			slog.Error("Server certificate and key files must be provided when running in secure mode")
			flag.Usage()
			os.Exit(1)
		}
		if *whisperPath == "" {
			slog.Error("Whisper executable path must be provided when running in server mode")
			flag.Usage()
			os.Exit(1)
		}
		if *whisperModel == "" {
			slog.Error("Whisper model path must be provided when running in server mode")
			flag.Usage()
			os.Exit(1)
		}

		// Initialize Scribe
		scribeConfig := scribe.Config{
			CertFile:      *serverCertFile,
			KeyFile:       *serverKeyFile,
			RecordingsDir: "recordings",
			HTTPAddr:      ":8444",
			WhisperPath:   *whisperPath,
			WhisperModel:  *whisperModel,
			Workers:       2,
		}

		scribeService, err := scribe.New(scribeConfig)
		if err != nil {
			slog.Error("Failed to initialize Scribe", "error", err)
			os.Exit(1)
		}

		// Start Scribe in its own goroutine
		go func() {
			if err := scribeService.Start(ctx); err != nil {
				slog.Error("Scribe service failed", "error", err)
			}
		}()

		// Ensure Scribe is stopped on shutdown
		defer func() {
			if err := scribeService.Stop(context.Background()); err != nil {
				slog.Error("Failed to stop Scribe service", "error", err)
			}
		}()

		// Launch your existing server
		libaserv.Launch(ctx, *serverCertFile, token, *serverKeyFile, clientList)
	} else {
		if !*insecureMode && *serverCertFile == "" {
			slog.Error("Server certificate file must be provided when not in insecure mode")
			flag.Usage()
			os.Exit(1)
		}
		libascli.Launch(ctx, *serverAddr, *insecureMode, token, *serverCertFile, *deviceID)
	}

	slog.Debug("Program exiting")
}
