package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildOggPage(packets ...[]byte) []byte {
	var page bytes.Buffer
	page.WriteString("OggS")
	page.WriteByte(0)           // version
	page.WriteByte(0)           // header type
	page.Write(make([]byte, 8)) // granule
	page.Write(make([]byte, 4)) // serial
	page.Write(make([]byte, 4)) // page seq
	page.Write(make([]byte, 4)) // checksum

	var lacing []byte
	var payload bytes.Buffer
	for _, packet := range packets {
		remaining := packet
		for len(remaining) >= 255 {
			lacing = append(lacing, 255)
			payload.Write(remaining[:255])
			remaining = remaining[255:]
		}
		lacing = append(lacing, byte(len(remaining)))
		payload.Write(remaining)
	}
	page.WriteByte(byte(len(lacing)))
	page.Write(lacing)
	page.Write(payload.Bytes())
	return page.Bytes()
}

func TestOggOpusReaderSkipsHeaders(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(buildOggPage([]byte("OpusHead\x01\x02")))
	stream.Write(buildOggPage([]byte("OpusTags\x00\x00")))
	stream.Write(buildOggPage([]byte{0x01, 0x02, 0x03}, []byte{0x04, 0x05}))

	reader := NewOggOpusReader(&stream)
	p1, err := reader.NextPacket()
	require.NoError(t, err)
	assert.Equal(t, []byte{0x01, 0x02, 0x03}, p1)

	p2, err := reader.NextPacket()
	require.NoError(t, err)
	assert.Equal(t, []byte{0x04, 0x05}, p2)
}

func TestWriteWAVAndStereoDownmix(t *testing.T) {
	mono := StereoInt16ToMonoBytes([]int16{1000, 3000, -2000, -4000})
	require.Len(t, mono, 4)
	assert.Equal(t, int16(2000), int16(binary.LittleEndian.Uint16(mono[0:2])))
	assert.Equal(t, int16(-3000), int16(binary.LittleEndian.Uint16(mono[2:4])))

	var wav bytes.Buffer
	require.NoError(t, WriteWAV(&wav, mono, 48000, 1))
	data := wav.Bytes()
	assert.Equal(t, "RIFF", string(data[0:4]))
	assert.Equal(t, "WAVE", string(data[8:12]))
	assert.Equal(t, 44+len(mono), len(data))
}

func TestTTSProvidersAgainstMockHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x01, 0x02, 0x03, 0x04})
	}))
	defer server.Close()

	ctx := context.Background()

	eleven := &ElevenLabsTTS{APIKey: "k", BaseURL: server.URL, HTTPClient: server.Client()}
	pcm, err := eleven.Synthesize(ctx, "hello", "default", 1.0)
	require.NoError(t, err)
	assert.Len(t, pcm, 4)

	openai := &OpenAITTS{APIKey: "k", BaseURL: server.URL, HTTPClient: server.Client()}
	pcm, err = openai.Synthesize(ctx, "hello", "alloy", 1.2)
	require.NoError(t, err)
	assert.Len(t, pcm, 4)

	deepgram := &DeepgramTTS{APIKey: "k", BaseURL: server.URL, HTTPClient: server.Client()}
	pcm, err = deepgram.Synthesize(ctx, "hello", "default", 1.0)
	require.NoError(t, err)
	assert.Len(t, pcm, 4)
}

func TestOpenAISTTAgainstMockHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"hello from whisper"}`))
	}))
	defer server.Close()

	stt := &OpenAISTT{APIKey: "k", BaseURL: server.URL, HTTPClient: server.Client()}
	audio := make(chan []byte, 1)
	audio <- []byte{0x00, 0x01, 0x02, 0x03}
	close(audio)

	var results []TranscriptionResult
	err := stt.Transcribe(context.Background(), audio, 48000, func(res TranscriptionResult) {
		results = append(results, res)
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, "hello from whisper", results[0].Text)
	assert.True(t, results[0].IsFinal)
}
