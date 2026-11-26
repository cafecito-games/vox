package voice

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// OggOpusReader demuxes raw Opus frames from an Ogg-Opus stream (such as FFmpeg
// stdout with `-f ogg -c:a libopus`) in pure Go with zero CGO dependencies.
type OggOpusReader struct {
	reader  io.Reader
	pending [][]byte
	partial bytes.Buffer
}

// NewOggOpusReader creates an Ogg-Opus packet reader wrapping reader.
func NewOggOpusReader(reader io.Reader) *OggOpusReader {
	return &OggOpusReader{reader: reader}
}

// NextPacket returns the next Opus audio packet, skipping OpusHead/OpusTags headers.
func (demux *OggOpusReader) NextPacket() ([]byte, error) {
	for {
		if len(demux.pending) > 0 {
			packet := demux.pending[0]
			demux.pending = demux.pending[1:]
			if isOpusHeader(packet) {
				continue
			}
			return packet, nil
		}
		if err := demux.readPage(); err != nil {
			return nil, err
		}
	}
}

func isOpusHeader(packet []byte) bool {
	return bytes.HasPrefix(packet, []byte("OpusHead")) || bytes.HasPrefix(packet, []byte("OpusTags"))
}

func (demux *OggOpusReader) readPage() error {
	var header [27]byte
	if _, err := io.ReadFull(demux.reader, header[:]); err != nil {
		return err
	}
	if string(header[0:4]) != "OggS" {
		return errors.New("invalid OggS page capture pattern")
	}
	numSegments := int(header[26])
	lacing := make([]byte, numSegments)
	if _, err := io.ReadFull(demux.reader, lacing); err != nil {
		return err
	}
	payloadLen := 0
	for _, seg := range lacing {
		payloadLen += int(seg)
	}
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(demux.reader, payload); err != nil {
		return err
	}

	offset := 0
	for _, seg := range lacing {
		segLen := int(seg)
		demux.partial.Write(payload[offset : offset+segLen])
		offset += segLen
		if segLen < 255 {
			packet := make([]byte, demux.partial.Len())
			copy(packet, demux.partial.Bytes())
			demux.partial.Reset()
			demux.pending = append(demux.pending, packet)
		}
	}
	return nil
}

// WriteWAV writes a standard 16-bit PCM RIFF/WAV file to writer.
func WriteWAV(writer io.Writer, pcm []byte, sampleRate, channels int) error {
	if sampleRate <= 0 || sampleRate > 192000 || channels <= 0 || channels > 8 {
		return fmt.Errorf("invalid WAV parameters: sampleRate=%d channels=%d", sampleRate, channels)
	}
	const bitsPerSample = 16
	const maxWAVPayload = 0xFFFFFFFF - 36
	if len(pcm) > maxWAVPayload {
		return fmt.Errorf("PCM payload exceeds WAV 4 GB limit: %d bytes", len(pcm))
	}
	byteRate := uint32(sampleRate * channels * (bitsPerSample / 8)) //nolint:gosec // bounded by validation above
	blockAlign := uint16(channels * (bitsPerSample / 8))            //nolint:gosec // bounded by validation above
	dataSize := uint32(len(pcm))                                    //nolint:gosec // bounded by maxWAVPayload check
	chunkSize := 36 + dataSize

	var header bytes.Buffer
	header.WriteString("RIFF")
	_ = binary.Write(&header, binary.LittleEndian, chunkSize)
	header.WriteString("WAVE")
	header.WriteString("fmt ")
	_ = binary.Write(&header, binary.LittleEndian, uint32(16))
	_ = binary.Write(&header, binary.LittleEndian, uint16(1))          // PCM
	_ = binary.Write(&header, binary.LittleEndian, uint16(channels))   //nolint:gosec // bounded 1..8
	_ = binary.Write(&header, binary.LittleEndian, uint32(sampleRate)) //nolint:gosec // bounded 1..192000
	_ = binary.Write(&header, binary.LittleEndian, byteRate)
	_ = binary.Write(&header, binary.LittleEndian, blockAlign)
	_ = binary.Write(&header, binary.LittleEndian, uint16(bitsPerSample))
	header.WriteString("data")
	_ = binary.Write(&header, binary.LittleEndian, dataSize)

	if _, err := writer.Write(header.Bytes()); err != nil {
		return err
	}
	_, err := writer.Write(pcm)
	return err
}

// StereoInt16ToMonoBytes downmixes 16-bit interleaved stereo PCM into mono little-endian s16le bytes.
func StereoInt16ToMonoBytes(stereo []int16) []byte {
	samples := len(stereo) / 2
	out := make([]byte, samples*2)
	for i := range samples {
		left := int32(stereo[i*2])
		right := int32(stereo[i*2+1])
		mono := int16((left + right) / 2)                      //nolint:gosec // average of two int16 values fits in int16
		binary.LittleEndian.PutUint16(out[i*2:], uint16(mono)) //nolint:gosec // two's-complement bitcast for s16le PCM
	}
	return out
}

// Int16ToLittleEndianBytes serializes a PCM int16 slice into raw little-endian bytes.
func Int16ToLittleEndianBytes(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample)) //nolint:gosec // two's-complement bitcast for s16le PCM
	}
	return out
}
