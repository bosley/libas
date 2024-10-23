package scribe

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
)

func getCurrentDateDir() string {
	return time.Now().Format("20060102")
}

func (s *Scribe) getCurrentDayPath() string {
	return filepath.Join(s.config.RecordingsDir, getCurrentDateDir())
}

func (s *Scribe) watchFiles(ctx context.Context) {
	defer s.watcher.Close()

	// Start watching the recordings directory
	if err := s.watcher.Add(s.config.RecordingsDir); err != nil {
		slog.Error("Failed to start watching recordings directory",
			"error", err,
			"path", s.config.RecordingsDir)
		return
	}

	slog.Info("Started watching recordings directory",
		"path", s.config.RecordingsDir)

	// Create and watch today's directory if it doesn't exist
	currentDayPath := s.getCurrentDayPath()
	if err := os.MkdirAll(currentDayPath, 0755); err != nil {
		slog.Error("Failed to create current day directory",
			"error", err,
			"path", currentDayPath)
		return
	}

	if err := s.watcher.Add(currentDayPath); err != nil {
		slog.Error("Failed to watch current day directory",
			"error", err,
			"path", currentDayPath)
		return
	}

	slog.Info("Watching current day directory", "path", currentDayPath)

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}

			// Handle the file system event
			if err := s.handleFSEvent(event); err != nil {
				slog.Error("Failed to handle file system event",
					"error", err,
					"event", event)
			}

		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("File watcher error", "error", err)
		}
	}
}

func (s *Scribe) handleFSEvent(event fsnotify.Event) error {
	// Skip temporary files and non-create events
	if strings.HasSuffix(event.Name, ".tmp") || event.Op != fsnotify.Create {
		return nil
	}

	// Get the relative path from the recordings directory
	relPath, err := filepath.Rel(s.config.RecordingsDir, event.Name)
	if err != nil {
		return fmt.Errorf("failed to get relative path: %w", err)
	}

	// Split the path into components
	parts := strings.Split(relPath, string(filepath.Separator))
	if len(parts) < 2 {
		return nil
	}

	// Only process events from today's directory
	if parts[0] != getCurrentDateDir() {
		return nil
	}

	// If this is a new directory in today's folder, watch it
	if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
		if err := s.watcher.Add(event.Name); err != nil {
			slog.Error("Failed to watch new directory",
				"error", err,
				"path", event.Name)
		} else {
			slog.Info("Watching new directory", "path", event.Name)
		}
	}

	// Handle client directory creation
	if len(parts) == 2 {
		if _, err := uuid.Parse(parts[1]); err == nil {
			slog.Info("Found new client directory", "clientID", parts[1])
			return s.handleNewClient(parts[1], event.Name)
		}
		return nil
	}

	// Handle new WAV files
	if len(parts) == 3 {
		clientID := parts[1]
		if _, err := uuid.Parse(clientID); err == nil && strings.HasSuffix(parts[2], ".wav") {
			if strings.Contains(parts[2], "_whisper") {
				slog.Info("Found new WAV file",
					"clientID", clientID,
					"file", parts[2])
				return s.handleNewAudioFile(clientID, event.Name)
			} else {
				slog.Warn("WAV file does not contain '_whisper', skipping",
					"clientID", clientID,
					"file", parts[2])
				return nil
			}
		}
	}

	return nil
}

func (s *Scribe) handleNewClient(clientID, fullPath string) error {
	// Add the client directory to the watcher using the full path
	if err := s.watcher.Add(fullPath); err != nil {
		return fmt.Errorf("failed to watch client directory: %w", err)
	}

	// Initialize client transcriptions
	s.clients.Store(clientID, &ClientTranscriptions{
		Messages: make([]TranscriptionMessage, 0),
	})

	slog.Info("New client directory detected and watching",
		"clientID", clientID,
		"path", fullPath)
	return nil
}

func (s *Scribe) handleNewAudioFile(clientID, filePath string) error {
	// Create a new transcription job
	job := TranscriptionJob{
		FilePath:  filePath,
		ClientID:  clientID,
		Timestamp: time.Now(),
	}

	// Add the job to the processing queue
	select {
	case s.queue <- job:
		slog.Info("Queued new audio file for processing",
			"clientID", clientID,
			"file", filepath.Base(filePath))
	default:
		return fmt.Errorf("job queue is full")
	}

	return nil
}
