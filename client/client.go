package libascli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"log/slog"
	"math"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/gordonklaus/portaudio"
)

const (
	calibrationDuration  = 5 * time.Second
	silenceThreshold     = 1 * time.Second
	vadThreshold         = 2.22 // TODO: make this configurable at a later date, I want to be able to change this while we are running on a per client basis  in case there's a multiple instantiations of clients in a single application
	backgroundBufferSize = 50   // TODO: as above so below

	sampleRate      = 44100
	channels        = 1
	framesPerBuffer = 1024
)

type AudioProcessor struct {
	backgroundNoise  float64
	backgroundBuffer []float64
	isTransmitting   bool
	lastNoiseTime    time.Time
	totalSamples     int
	totalBytes       int
	logCounter       int
	clientID         uuid.UUID
}

func NewAudioProcessor() *AudioProcessor {
	return &AudioProcessor{
		backgroundBuffer: make([]float64, 0, backgroundBufferSize),
	}
}

func (ap *AudioProcessor) calibrateBackgroundNoise() {
	slog.Debug("Calibrating background noise")

	var totalAmplitude float64
	var sampleCount int

	stream, err := portaudio.OpenDefaultStream(channels, 0, sampleRate, framesPerBuffer, func(in []int16) {
		amplitude := calculateChunkAmplitude(in)
		totalAmplitude += amplitude
		sampleCount++
	})
	if err != nil {
		slog.Error("Failed to open calibration stream", "error", err)
		return
	}
	defer stream.Close()

	err = stream.Start()
	if err != nil {
		slog.Error("Failed to start calibration stream", "error", err)
		return
	}

	time.Sleep(calibrationDuration)

	err = stream.Stop()
	if err != nil {
		slog.Error("Failed to stop calibration stream", "error", err)
	}

	ap.backgroundNoise = totalAmplitude / float64(sampleCount)
	slog.Debug("Background noise calibration complete", "averageAmplitude", ap.backgroundNoise)
}

func (ap *AudioProcessor) processAudioChunk(ctx context.Context, cancel context.CancelFunc, conn net.Conn, chunk []int16, connClosed chan struct{}) {
	select {
	case <-ctx.Done():
		return
	default:
		chunkAmplitude := calculateChunkAmplitude(chunk)
		ap.updateBackgroundNoise(chunkAmplitude)

		ap.logCounter++
		if ap.logCounter%10 == 0 {
			slog.Debug("Audio chunk received",
				"chunkAmplitude", chunkAmplitude,
				"backgroundNoise", ap.backgroundNoise,
				"ratio", chunkAmplitude/ap.backgroundNoise)
		}

		energyRatio := chunkAmplitude / ap.backgroundNoise
		isSpeech := energyRatio > vadThreshold

		if isSpeech {
			ap.lastNoiseTime = time.Now()
			if !ap.isTransmitting {
				ap.isTransmitting = true
				ap.totalSamples = 0
				ap.totalBytes = 0
				slog.Info("Speech detected, starting transmission",
					"chunkAmplitude", chunkAmplitude,
					"backgroundNoise", ap.backgroundNoise,
					"ratio", energyRatio)
				sendStartTransmission(conn)
			}
			if err := sendAudioChunk(ctx, conn, chunk); err != nil {
				if isConnectionClosed(err) {
					select {
					case connClosed <- struct{}{}: // Signal connection closure
					default: // Channel already closed or full
					}
					return
				}
				slog.Error("Error sending audio chunk", "error", err)
			}
			ap.totalSamples += len(chunk)
			ap.totalBytes += len(chunk) * 2 // 2 bytes per sample
		} else if ap.isTransmitting {
			// Continue transmitting during short pauses
			if err := sendAudioChunk(ctx, conn, chunk); err != nil {
				if isConnectionClosed(err) {
					select {
					case connClosed <- struct{}{}: // Signal connection closure
					default: // Channel already closed or full
					}
					return
				}
				slog.Error("Error sending audio chunk", "error", err)
			}
			ap.totalSamples += len(chunk)
			ap.totalBytes += len(chunk) * 2

			// Check for extended silence
			if time.Since(ap.lastNoiseTime) > silenceThreshold {
				ap.isTransmitting = false
				slog.Info("Extended silence detected, stopping transmission",
					"totalSamples", ap.totalSamples,
					"totalBytes", ap.totalBytes,
					"durationSeconds", time.Since(ap.lastNoiseTime).Seconds())
				sendEndTransmission(conn)
			}
		}

		if ap.isTransmitting && ap.logCounter%10 == 0 {
			slog.Debug("Transmitting audio",
				"totalSamples", ap.totalSamples,
				"totalBytes", ap.totalBytes,
				"durationSeconds", time.Since(ap.lastNoiseTime).Seconds())
		}
	}
}

func (ap *AudioProcessor) updateBackgroundNoise(amplitude float64) {
	if len(ap.backgroundBuffer) >= backgroundBufferSize {
		ap.backgroundBuffer = ap.backgroundBuffer[1:]
	}
	ap.backgroundBuffer = append(ap.backgroundBuffer, amplitude)

	// Calculate new background noise level
	var sum float64
	for _, a := range ap.backgroundBuffer {
		sum += a
	}
	ap.backgroundNoise = sum / float64(len(ap.backgroundBuffer))
}

func calculateChunkAmplitude(chunk []int16) float64 {
	var totalAmplitude float64
	for _, sample := range chunk {
		totalAmplitude += math.Abs(float64(sample))
	}
	return totalAmplitude / float64(len(chunk))
}

func sendStartTransmission(conn net.Conn) {
	_, err := conn.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF}) // Start marker
	if err != nil {
		slog.Error("Failed to send start transmission marker", "error", err)
	}
}

func sendEndTransmission(conn net.Conn) {
	_, err := conn.Write([]byte{0x00, 0x00, 0x00, 0x00}) // End marker
	if err != nil {
		slog.Error("Failed to send end transmission marker", "error", err)
	}
}

func sendAudioChunk(ctx context.Context, conn net.Conn, chunk []int16) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Send chunk size
		sizeBuf := make([]byte, 4)
		binary.BigEndian.PutUint32(sizeBuf, uint32(len(chunk)*2))
		if _, err := conn.Write(sizeBuf); err != nil {
			return err
		}

		// Send audio data
		bytes := make([]byte, len(chunk)*2)
		for i, sample := range chunk {
			binary.LittleEndian.PutUint16(bytes[i*2:], uint16(sample))
		}
		if _, err := conn.Write(bytes); err != nil {
			return err
		}
		return nil
	}
}

// Helper function to check for connection closure
func isConnectionClosed(err error) bool {
	if err == io.EOF {
		return true
	}
	if netErr, ok := err.(*net.OpError); ok {
		if netErr.Err.Error() == "broken pipe" {
			return true
		}
	}
	return false
}

func ListAudioDevices() ([]portaudio.DeviceInfo, error) {
	err := portaudio.Initialize()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize PortAudio: %w", err)
	}
	defer portaudio.Terminate()

	devices, err := portaudio.Devices()
	if err != nil {
		return nil, fmt.Errorf("failed to get devices: %w", err)
	}

	// Filter to only input devices
	inputDevices := make([]portaudio.DeviceInfo, 0)
	for _, device := range devices {
		if device.MaxInputChannels > 0 {
			inputDevices = append(inputDevices, *device)
		}
	}

	return inputDevices, nil
}

func Launch(ctx context.Context, serverAddr string, insecureMode bool, token, serverCertFile string, deviceID int) {
	slog.Debug("Starting client",
		"serverAddress", serverAddr,
		"deviceID", deviceID)

	// Create TLS configuration
	tlsConfig, err := createTLSConfig(insecureMode, serverCertFile)
	if err != nil {
		slog.Error("Failed to create TLS config", "error", err)
		return
	}

	// Create a new context with cancellation for this launch
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Create connection monitor channel
	connClosed := make(chan struct{})

	// Start a goroutine to monitor connection status
	go func() {
		// Wait for either context cancellation or connection closure
		select {
		case <-ctx.Done():
			return
		case <-connClosed:
			slog.Error("Server connection lost")
			cancel() // Cancel context to trigger shutdown
			return
		}
	}()

	// Establish a persistent TLS connection
	dialer := &tls.Dialer{
		Config: tlsConfig,
	}
	conn, err := dialer.DialContext(ctx, "tcp", serverAddr)
	if err != nil {
		slog.Error("Failed to connect to server", "error", err)
		return
	}
	defer conn.Close()

	// Send the token to the server
	_, err = conn.Write([]byte(token))
	if err != nil {
		slog.Error("Failed to send token to server", "error", err)
		return
	}

	// Receive client ID from server
	clientID, err := receiveClientID(conn)
	if err != nil {
		slog.Error("Failed to receive client ID", "error", err)
		return
	}
	slog.Info("Received client ID", "clientID", clientID)

	err = portaudio.Initialize()
	if err != nil {
		slog.Error("Failed to initialize PortAudio", "error", err)
		return
	}
	defer portaudio.Terminate()

	var inputParams portaudio.StreamParameters
	if deviceID > 0 { // Only use specific device if explicitly requested (non-zero)
		devices, err := portaudio.Devices()
		if err != nil {
			slog.Error("Failed to get audio devices", "error", err)
			return
		}

		if deviceID >= len(devices) {
			slog.Error("Invalid device ID", "deviceID", deviceID)
			return
		}

		device := devices[deviceID]
		if device.MaxInputChannels == 0 {
			slog.Error("Specified device is not an input device",
				"deviceID", deviceID,
				"deviceName", device.Name)
			return
		}

		slog.Info("Using specified audio device",
			"deviceID", deviceID,
			"deviceName", device.Name,
			"sampleRate", device.DefaultSampleRate,
			"inputChannels", device.MaxInputChannels)

		inputParams = portaudio.StreamParameters{
			Input: portaudio.StreamDeviceParameters{
				Device:   device,
				Channels: channels,
				Latency:  device.DefaultLowInputLatency,
			},
			SampleRate:      sampleRate,
			FramesPerBuffer: framesPerBuffer,
		}
	} else {
		// Use default device
		defaultDevice, err := portaudio.DefaultInputDevice()
		if err != nil {
			slog.Error("Failed to get default input device", "error", err)
			return
		}

		slog.Info("Using default audio device",
			"deviceName", defaultDevice.Name,
			"sampleRate", defaultDevice.DefaultSampleRate,
			"inputChannels", defaultDevice.MaxInputChannels)

		inputParams = portaudio.StreamParameters{
			Input: portaudio.StreamDeviceParameters{
				Device:   defaultDevice,
				Channels: channels,
				Latency:  defaultDevice.DefaultLowInputLatency,
			},
			SampleRate:      sampleRate,
			FramesPerBuffer: framesPerBuffer,
		}
	}

	ap := NewAudioProcessor()
	ap.clientID = clientID
	ap.calibrateBackgroundNoise()

	// Open the stream with our parameters
	stream, err := portaudio.OpenStream(inputParams, func(in []int16) {
		select {
		case <-ctx.Done():
			return
		default:
			ap.processAudioChunk(ctx, cancel, conn, in, connClosed)
		}
	})
	if err != nil {
		slog.Error("Failed to open audio stream", "error", err)
		return
	}
	defer stream.Close()

	// Start the audio stream
	err = stream.Start()
	if err != nil {
		slog.Error("Failed to start audio stream", "error", err)
		return
	}

	// Wait for context cancellation
	<-ctx.Done()
	slog.Debug("Client shutting down")

	// Stop the audio stream
	err = stream.Stop()
	if err != nil {
		slog.Error("Failed to stop audio stream", "error", err)
	}
}

func receiveClientID(conn net.Conn) (uuid.UUID, error) {
	idBytes := make([]byte, 16)
	_, err := io.ReadFull(conn, idBytes)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.FromBytes(idBytes)
}

func createTLSConfig(insecureMode bool, serverCertFile string) (*tls.Config, error) {
	if insecureMode {
		slog.Warn("Running in insecure mode. This should not be used in production!")
		return &tls.Config{InsecureSkipVerify: true}, nil
	}

	// Load the server's certificate
	certPEM, err := ioutil.ReadFile(serverCertFile)
	if err != nil {
		return nil, err
	}

	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(certPEM) {
		return nil, fmt.Errorf("failed to append server certificate")
	}

	return &tls.Config{
		RootCAs: certPool,
	}, nil
}
