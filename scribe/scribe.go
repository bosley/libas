package scribe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"sync"

	"github.com/fsnotify/fsnotify"
	"github.com/gorilla/websocket"
)

// Configuration for the Scribe service
type Config struct {
	// Certificate files for TLS
	CertFile string
	KeyFile  string

	// Base directory to monitor for recordings
	RecordingsDir string

	// HTTP server address
	HTTPAddr string

	// Path to whisper executable
	WhisperPath string

	// Path to whisper model
	WhisperModel string

	// Number of worker threads for processing
	Workers int
}

// Scribe manages the transcription service
type Scribe struct {
	config Config

	// File system watcher
	watcher *fsnotify.Watcher

	// Transcription management
	clients     sync.Map // map[string]*ClientTranscriptions
	subscribers sync.Map // map[string][]*wsConnection

	// Processing queue
	queue   chan TranscriptionJob
	workers sync.WaitGroup

	// HTTP/Websocket
	server   *http.Server
	upgrader websocket.Upgrader
}

// New creates a new Scribe instance
func New(cfg Config) (*Scribe, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 2
	}

	// Load TLS certificates
	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificates: %w", err)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	s := &Scribe{
		config:  cfg,
		watcher: watcher,
		queue:   make(chan TranscriptionJob, 100),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true // TODO: Implement proper origin checking
			},
		},
		server: &http.Server{
			Addr:      cfg.HTTPAddr,
			TLSConfig: tlsConfig,
		},
	}

	return s, nil
}

// Start begins the Scribe service
func (s *Scribe) Start(ctx context.Context) error {
	// Start the worker pool
	for i := 0; i < s.config.Workers; i++ {
		s.workers.Add(1)
		go s.worker(ctx)
	}

	// Start the file system watcher
	go s.watchFiles(ctx)

	// Start the HTTP server
	return s.startHTTP(ctx)
}

// Stop gracefully shuts down the Scribe service
func (s *Scribe) Stop(ctx context.Context) error {
	// Stop accepting new jobs
	close(s.queue)

	// Wait for workers to finish
	done := make(chan struct{})
	go func() {
		s.workers.Wait()
		close(done)
	}()

	// Wait for workers or context timeout
	select {
	case <-done:
	case <-ctx.Done():
		return fmt.Errorf("shutdown timed out")
	}

	// Stop the HTTP server
	if s.server != nil {
		if err := s.server.Shutdown(ctx); err != nil {
			return fmt.Errorf("failed to stop HTTP server: %w", err)
		}
	}

	// Close the file watcher
	if err := s.watcher.Close(); err != nil {
		return fmt.Errorf("failed to close file watcher: %w", err)
	}

	return nil
}
