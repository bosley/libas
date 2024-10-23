package libaserv

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bosley/libas/audio"
	"github.com/google/uuid"
)

const (
	defaultServerAddr = "localhost:8443"
)

var (
	dailyDirMutex sync.Mutex
	currentDay    string
)

func Launch(ctx context.Context, certFile, token, keyFile string, clientList *ClientList) {
	slog.Debug("Starting server", "address", defaultServerAddr)

	updateCurrentDay()

	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		slog.Error("Failed to load server certificate and key", "error", err)
		slog.Error("Please ensure you're using proper TLS certificates. If you're testing locally, you can generate self-signed certificates or use the -insecure flag for non-TLS connections.")
		return
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}

	listener, err := tls.Listen("tcp", defaultServerAddr, tlsConfig)
	if err != nil {
		slog.Error("Failed to start TLS server", "error", err)
		return
	}

	done := make(chan struct{})

	go func() {
		<-ctx.Done()
		slog.Debug("Server shutting down")
		listener.Close()
		close(done)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				slog.Debug("Server stopped accepting new connections")
				os.Exit(0)
			default:
				slog.Error("Failed to accept connection", "error", err)
				break
			}
		}

		go handleNewConnection(token, ctx, conn, clientList)
	}
}

func handleNewConnection(token string, ctx context.Context, conn net.Conn, clientList *ClientList) {
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()

	if conn == nil || clientList == nil {
		return
	}

	tokenBuffer := make([]byte, len(token))
	_, err := io.ReadFull(conn, tokenBuffer)
	if err != nil {
		slog.Error("Failed to read token from client", "error", err, "remoteAddr", conn.RemoteAddr())
		return
	}

	if string(tokenBuffer) != token {
		slog.Warn("Invalid token received", "remoteAddr", conn.RemoteAddr())
		return
	}

	clientID := uuid.New()
	client := &Client{
		ID:   clientID,
		Addr: conn.RemoteAddr().String(),
	}
	clientList.Add(client)

	handleConnection(ctx, conn, clientID, clientList)
}

func handleConnection(ctx context.Context, conn net.Conn, clientID uuid.UUID, clientList *ClientList) {
	slog.Debug("New client connected", "clientID", clientID, "remoteAddr", conn.RemoteAddr())
	defer func() {
		conn.Close()
		clientList.Remove(clientID)
		slog.Debug("Client connection closed", "clientID", clientID, "remoteAddr", conn.RemoteAddr())
	}()

	if err := sendClientID(conn, clientID); err != nil {
		slog.Error("Failed to send client ID", "error", err, "clientID", clientID)
		return
	}

	transmissionBuffer := []byte{}
	isReceivingTransmission := false
	var file *os.File
	var transmissionStartTime time.Time

	defer func() {
		if file != nil {
			file.Close()
			slog.Debug("Closed file due to connection end", "clientID", clientID)
		}
	}()

	//	lastFileFinish := time.Now()

	finishCurrentFile := func() {
		if file != nil {
			// Update WAV header with final file size
			if err := audio.UpdateWavHeader(file, uint32(len(transmissionBuffer))); err != nil {
				slog.Error("Failed to update WAV header", "error", err, "clientID", clientID)
			}
			fileName := file.Name()
			file.Close()
			file = nil

			// Resample the file for Whisper
			if err := audio.ResampleForWhisper(fileName); err != nil {
				slog.Error("Failed to resample audio for Whisper", "error", err, "clientID", clientID)
			} else {
				slog.Info("Audio resampled for Whisper", "file", fileName)
			}
		}
		//	lastFileFinish = time.Now()
	}

	startFile := func() error {
		var err error
		file, err = createWavFile(clientID)
		if err != nil {
			slog.Error("Failed to create WAV file", "error", err, "clientID", clientID)
			return err
		}
		// Write WAV header
		if err := audio.WriteWavHeader(file, 0); err != nil {
			slog.Error("Failed to write WAV header", "error", err, "clientID", clientID)
			return err
		}
		return err
	}

	for {
		marker := make([]byte, 4)
		_, err := io.ReadFull(conn, marker)
		if err != nil {
			if err == io.EOF {
				slog.Debug("Client disconnected", "clientID", clientID, "remoteAddr", conn.RemoteAddr())
			} else {
				slog.Error("Failed to read marker", "error", err, "clientID", clientID, "remoteAddr", conn.RemoteAddr())
			}
			if isReceivingTransmission && file != nil {
				handleIncompleteTransmission(file, transmissionStartTime, clientID)
			}
			return
		}

		if binary.BigEndian.Uint32(marker) == 0xFFFFFFFF {
			isReceivingTransmission = true
			transmissionBuffer = make([]byte, 0)
			transmissionStartTime = time.Now()

			if err := startFile(); err != nil {
				return
			}

			slog.Info("Started receiving new transmission", "clientID", clientID, "remoteAddr", conn.RemoteAddr())
		} else if binary.BigEndian.Uint32(marker) == 0x00000000 {
			isReceivingTransmission = false
			transmissionDuration := time.Since(transmissionStartTime)

			if transmissionDuration < time.Second {
				slog.Debug("Dropping short transmission",
					"duration", transmissionDuration.Seconds(),
					"bytes", len(transmissionBuffer),
					"clientID", clientID,
					"remoteAddr", conn.RemoteAddr())
				if file != nil {
					file.Close()
					os.Remove(file.Name())
				}
			} else {
				slog.Info("Finished receiving transmission",
					"duration", transmissionDuration.Seconds(),
					"bytes", len(transmissionBuffer),
					"clientID", clientID,
					"remoteAddr", conn.RemoteAddr())

				finishCurrentFile()
			}
		} else if isReceivingTransmission {
			chunkSize := binary.BigEndian.Uint32(marker)

			chunkData := make([]byte, chunkSize)
			_, err := io.ReadFull(conn, chunkData)
			if err != nil {
				slog.Error("Failed to read chunk data", "error", err, "clientID", clientID, "remoteAddr", conn.RemoteAddr())
				return
			}

			transmissionBuffer = append(transmissionBuffer, chunkData...)
			if file != nil {
				_, err = file.Write(chunkData)
				if err != nil {
					slog.Error("Failed to write chunk data to file", "error", err, "clientID", clientID)
				}
			}

			//		now := time.Now()
			//		if now.Sub(lastFileFinish) > 1*time.Second {
			//			slog.Debug("submission shortcut")
			//			finishCurrentFile() // This will update lastFileFinish
			//			if err := startFile(); err != nil {
			//				slog.Error("Failed to start new file", "error", err, "clientID", clientID)
			//				return
			//			}
			//		}

		}

		select {
		case <-ctx.Done():
			slog.Debug("Connection handler shutting down", "clientID", clientID, "remoteAddr", conn.RemoteAddr())
			return
		default:
		}
	}
}

func createWavFile(clientID uuid.UUID) (*os.File, error) {
	updateCurrentDay()

	dailyDir := filepath.Join("recordings", currentDay)
	clientDir := filepath.Join(dailyDir, clientID.String())

	dailyDirMutex.Lock()
	defer dailyDirMutex.Unlock()

	err := os.MkdirAll(clientDir, 0755)
	if err != nil {
		return nil, fmt.Errorf("failed to create client directory: %w", err)
	}

	timestamp := time.Now().Format("150405") // HHMMSS
	filename := fmt.Sprintf("audio_%s.wav", timestamp)
	return os.Create(filepath.Join(clientDir, filename))
}

func updateCurrentDay() {
	newDay := time.Now().Format("20060102") // YYYYMMDD
	if newDay != currentDay {
		dailyDirMutex.Lock()
		defer dailyDirMutex.Unlock()

		if newDay != currentDay {
			currentDay = newDay
			dailyDir := filepath.Join("recordings", currentDay)
			err := os.MkdirAll(dailyDir, 0755)
			if err != nil {
				slog.Error("Failed to create daily directory", "error", err, "path", dailyDir)
			} else {
				slog.Info("Created new daily directory", "path", dailyDir)
			}
		}
	}
}

func sendClientID(conn net.Conn, clientID uuid.UUID) error {
	_, err := conn.Write(clientID[:])
	return err
}

func handleIncompleteTransmission(file *os.File, startTime time.Time, clientID uuid.UUID) {
	transmissionDuration := time.Since(startTime)
	if transmissionDuration < time.Second {
		slog.Debug("Dropping incomplete short transmission",
			"duration", transmissionDuration.Seconds(),
			"clientID", clientID)
		file.Close()
		os.Remove(file.Name())
	} else {
		slog.Info("Saving incomplete transmission",
			"duration", transmissionDuration.Seconds(),
			"clientID", clientID)
		file.Close()
		newName := file.Name() + ".incomplete"
		os.Rename(file.Name(), newName)
	}
}
