package scribe

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

func (s *Scribe) worker(ctx context.Context) {
	slog.Debug("Worker starting")
	defer func() {
		slog.Debug("Worker shutting down")
		s.workers.Done()
	}()

	for {
		select {
		case <-ctx.Done():
			slog.Debug("Worker context cancelled")
			return

		case job, ok := <-s.queue:
			if !ok {
				slog.Debug("Worker queue closed")
				return
			}

			if err := s.processJob(ctx, job); err != nil {
				slog.Error("Failed to process transcription job",
					"error", err,
					"file", job.FilePath,
					"clientID", job.ClientID)
			}
		}
	}
}

func (s *Scribe) processJob(ctx context.Context, job TranscriptionJob) error {
	slog.Info("Processing audio file",
		"file", job.FilePath,
		"clientID", job.ClientID)

	// Execute whisper command
	cmd := exec.CommandContext(ctx, s.config.WhisperPath,
		"--model", s.config.WhisperModel,
		job.FilePath)

	slog.Debug("Executing whisper command",
		"command", cmd.String(),
		"args", cmd.Args)

	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := string(exitErr.Stderr)
			// Check for file not found error
			if strings.Contains(stderr, "input file not found") {
				slog.Info("Audio file not found (likely processed or deleted)",
					"file", job.FilePath,
					"clientID", job.ClientID)
				return nil
			}
			slog.Debug("Whisper command failed",
				"stderr", stderr,
				"exitCode", exitErr.ExitCode())
		}
		return fmt.Errorf("whisper execution failed: %w", err)
	}

	outputStr := string(output)
	slog.Debug("Whisper command output received",
		"outputLength", len(output),
		"output", outputStr)

	// Extract text from subtitle-style format
	text := extractText(outputStr)
	if text == "" {
		slog.Info("No transcribable content found",
			"file", job.FilePath,
			"clientID", job.ClientID)
		return nil
	}

	// Create transcription message
	msg := TranscriptionMessage{
		Timestamp:  job.Timestamp,
		Text:       text,
		AudioFile:  filepath.Base(job.FilePath),
		Confidence: 1.0,
	}

	// Store the transcription
	value, _ := s.clients.LoadOrStore(job.ClientID, &ClientTranscriptions{
		Messages: make([]TranscriptionMessage, 0),
	})
	transcriptions := value.(*ClientTranscriptions)
	transcriptions.Messages = append(transcriptions.Messages, msg)
	s.clients.Store(job.ClientID, transcriptions)

	// Prepare message for websocket
	wsMsg := WebSocketMessage{
		Type:      "transcription",
		ClientID:  job.ClientID,
		Timestamp: job.Timestamp,
		Payload:   msg,
	}

	data, err := json.Marshal(wsMsg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	// Notify subscribers
	if value, ok := s.subscribers.Load(job.ClientID); ok {
		connections := value.([]*wsConnection)
		for i, conn := range connections {
			select {
			case conn.send <- data:
				slog.Debug("Sent message to subscriber",
					"clientID", job.ClientID,
					"connectionIndex", i)
			default:
				slog.Warn("Failed to send to subscriber - channel full",
					"clientID", job.ClientID,
					"connectionIndex", i)
			}
		}
	} else {
		slog.Debug("No subscribers found for client", "clientID", job.ClientID)
	}

	slog.Info("Successfully transcribed audio",
		"clientID", job.ClientID,
		"file", filepath.Base(job.FilePath),
		"text", text)

	return nil
}

func extractText(output string) string {
	var builder strings.Builder
	lines := strings.Split(output, "\n")

	for _, line := range lines {

		// Skip empty lines
		if strings.TrimSpace(line) == "" {
			continue
		}

		// Skip blank audio markers
		if strings.Contains(line, "[BLANK_AUDIO]") {
			continue
		}

		text := strings.TrimSpace(line)
		if text != "" {
			if builder.Len() > 0 {
				builder.WriteString(" ")
			}
			builder.WriteString(text)
		}
	}

	return strings.TrimSpace(builder.String())
}
