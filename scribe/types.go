package scribe

import (
	"time"
)

// ClientTranscriptions holds all transcriptions for a client
type ClientTranscriptions struct {
	Messages []TranscriptionMessage
}

// TranscriptionMessage represents a single transcribed message
type TranscriptionMessage struct {
	Timestamp  time.Time `json:"timestamp"`
	Text       string    `json:"text"`
	AudioFile  string    `json:"audioFile"`
	Confidence float32   `json:"confidence"`
}

// TranscriptionJob represents a job for the worker pool
type TranscriptionJob struct {
	FilePath  string
	ClientID  string
	Timestamp time.Time
}

// WebSocketMessage represents a message sent over WebSocket
type WebSocketMessage struct {
	Type      string      `json:"type"`
	ClientID  string      `json:"clientId"`
	Timestamp time.Time   `json:"timestamp"`
	Payload   interface{} `json:"payload"`
}
