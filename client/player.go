package libascli

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/gordonklaus/portaudio"
	"github.com/youpy/go-wav"
)

func PlayAudioFile(filename string) error {
	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize PortAudio: %w", err)
	}
	defer portaudio.Terminate()

	file, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	reader := wav.NewReader(file)

	format, err := reader.Format()
	if err != nil {
		return err
	}
	buffer := make([]int16, framesPerBuffer)

	stream, err := portaudio.OpenDefaultStream(
		0,
		int(format.NumChannels),
		float64(format.SampleRate),
		framesPerBuffer,
		func(out []int16) {
			samples, err := reader.ReadSamples(uint32(len(buffer)))
			if err == io.EOF {
				for i := range out {
					out[i] = 0
				}
				return
			}
			if err != nil {
				slog.Error("Error reading from WAV file", "error", err)
				return
			}

			for i := 0; i < len(samples) && i < len(out); i++ {
				out[i] = int16(samples[i].Values[0])
			}
			// Fill remaining buffer with silence if needed
			for i := len(samples); i < len(out); i++ {
				out[i] = 0
			}
		},
	)
	if err != nil {
		return fmt.Errorf("failed to open audio stream: %w", err)
	}
	defer stream.Close()

	if err := stream.Start(); err != nil {
		return fmt.Errorf("failed to start audio stream: %w", err)
	}

	fmt.Println("Playing audio. Press Enter to stop...")
	fmt.Scanln()

	return stream.Stop()
}

func readInt16(r io.Reader, data []int16) (n int, err error) {
	buf := make([]byte, 2*len(data))
	n, err = io.ReadFull(r, buf)
	if err != nil {
		if err == io.EOF {
			return 0, err
		}
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
		n /= 2 // Convert byte count to int16 count
	} else {
		n /= 2 // Convert byte count to int16 count
	}
	for i := 0; i < n; i++ {
		data[i] = int16(buf[i*2]) | int16(buf[i*2+1])<<8
	}
	return
}
