package audio

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
)

const (
	recordingSampleRate = 44100 // Rate at which audio is being recorded
	whisperSampleRate   = 16000 // Rate required by Whisper
	channels            = 1     // Mono audio
	bitsPerSample       = 16    // Using int16 for samples
)

type WavHeader struct {
	ChunkID       [4]byte
	ChunkSize     uint32
	Format        [4]byte
	Subchunk1ID   [4]byte
	Subchunk1Size uint32
	AudioFormat   uint16
	NumChannels   uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
	Subchunk2ID   [4]byte
	Subchunk2Size uint32
}

func WriteWavHeader(file *os.File, dataSize uint32) error {
	header := WavHeader{
		ChunkID:       [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize:     dataSize + 36,
		Format:        [4]byte{'W', 'A', 'V', 'E'},
		Subchunk1ID:   [4]byte{'f', 'm', 't', ' '},
		Subchunk1Size: 16,
		AudioFormat:   1,
		NumChannels:   channels,
		SampleRate:    recordingSampleRate,
		ByteRate:      recordingSampleRate * uint32(channels) * uint32(bitsPerSample) / 8,
		BlockAlign:    channels * bitsPerSample / 8,
		BitsPerSample: bitsPerSample,
		Subchunk2ID:   [4]byte{'d', 'a', 't', 'a'},
		Subchunk2Size: dataSize,
	}

	return binary.Write(file, binary.LittleEndian, header)
}

func UpdateWavHeader(file *os.File, dataSize uint32) error {
	// Update ChunkSize (file size - 8)
	if _, err := file.Seek(4, 0); err != nil {
		return fmt.Errorf("failed to seek to ChunkSize: %w", err)
	}
	if err := binary.Write(file, binary.LittleEndian, uint32(dataSize+36)); err != nil {
		return fmt.Errorf("failed to write ChunkSize: %w", err)
	}

	// Update Subchunk2Size (data size)
	if _, err := file.Seek(40, 0); err != nil {
		return fmt.Errorf("failed to seek to Subchunk2Size: %w", err)
	}
	if err := binary.Write(file, binary.LittleEndian, dataSize); err != nil {
		return fmt.Errorf("failed to write Subchunk2Size: %w", err)
	}

	return nil
}

// ResampleForWhisper resamples the WAV file to 16kHz for Whisper
func ResampleForWhisper(inputPath string) error {
	outputPath := inputPath[:len(inputPath)-4] + "_whisper.wav"

	cmd := exec.Command("ffmpeg",
		"-i", inputPath,
		"-ar", fmt.Sprintf("%d", whisperSampleRate),
		"-ac", "1",
		"-y", // Overwrite output file
		outputPath)

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to resample audio: %w", err)
	}

	os.Remove(inputPath)

	return nil
}
