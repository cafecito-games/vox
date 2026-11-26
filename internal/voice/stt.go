package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// TranscriptionResult represents one interim or finalized speech transcript.
type TranscriptionResult struct {
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
	IsFinal    bool    `json:"is_final"`
	UserID     string  `json:"user_id,omitempty"`
}

// STTProvider transcribes a stream of mono signed 16-bit PCM chunks.
type STTProvider interface {
	Transcribe(ctx context.Context, audio <-chan []byte, sampleRate int, onResult func(TranscriptionResult)) error
	Close() error
}

// NewSTTProvider instantiates an STT provider by name ("deepgram" or "openai").
func NewSTTProvider(name string) (STTProvider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "deepgram":
		key, err := requireEnv("DEEPGRAM_API_KEY")
		if err != nil {
			return nil, err
		}
		model := envOrDefault("VOX_DEEPGRAM_MODEL", "nova-3")
		language := envOrDefault("VOX_DEEPGRAM_LANGUAGE", "multi")
		return &DeepgramSTT{
			APIKey:   key,
			Model:    model,
			Language: language,
		}, nil
	case "openai":
		key, err := requireEnv("OPENAI_API_KEY")
		if err != nil {
			return nil, err
		}
		return &OpenAISTT{
			APIKey:     key,
			HTTPClient: defaultHTTPClient(),
		}, nil
	default:
		return nil, fmt.Errorf("unknown STT provider %q (supported: deepgram, openai)", name)
	}
}

func envOrDefault(name, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(name)); v != "" {
		return v
	}
	return fallback
}

// DeepgramSTT streams mono PCM over Deepgram's live WebSocket API with automatic
// KeepAlive heartbeats so silent speakers do not trigger 1011 timeouts.
type DeepgramSTT struct {
	APIKey   string
	BaseURL  string
	Model    string
	Language string
}

// Transcribe opens a Deepgram streaming session for one speaker's PCM stream.
func (provider *DeepgramSTT) Transcribe(
	ctx context.Context,
	audio <-chan []byte,
	sampleRate int,
	onResult func(TranscriptionResult),
) error {
	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = "wss://api.deepgram.com/v1/listen"
	}
	model := provider.Model
	if model == "" {
		model = "nova-3"
	}
	language := provider.Language
	if language == "" {
		language = "multi"
	}
	if sampleRate <= 0 {
		sampleRate = 48000
	}

	params := url.Values{}
	params.Set("model", model)
	params.Set("language", language)
	params.Set("encoding", "linear16")
	params.Set("sample_rate", strconv.Itoa(sampleRate))
	params.Set("channels", "1")
	params.Set("interim_results", "true")
	params.Set("punctuate", "true")
	params.Set("smart_format", "true")

	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	headers := http.Header{"Authorization": []string{"Token " + provider.APIKey}}

	conn, resp, err := dialer.DialContext(ctx, baseURL+"?"+params.Encode(), headers)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return fmt.Errorf("connect Deepgram STT: %w", err)
	}
	defer func() { _ = conn.Close() }()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		keepAlive := time.NewTicker(3 * time.Second)
		defer keepAlive.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = conn.WriteJSON(map[string]string{"type": "Finalize"})
				_ = conn.WriteJSON(map[string]string{"type": "CloseStream"})
				return
			case chunk, ok := <-audio:
				if !ok {
					_ = conn.WriteJSON(map[string]string{"type": "Finalize"})
					_ = conn.WriteJSON(map[string]string{"type": "CloseStream"})
					return
				}
				if len(chunk) > 0 {
					if writeErr := conn.WriteMessage(websocket.BinaryMessage, chunk); writeErr != nil {
						return
					}
				}
			case <-keepAlive.C:
				if writeErr := conn.WriteJSON(map[string]string{"type": "KeepAlive"}); writeErr != nil {
					return
				}
			}
		}
	}()

	type deepgramResponse struct {
		Type    string `json:"type"`
		IsFinal bool   `json:"is_final"`
		Channel struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
		} `json:"channel"`
	}

	for {
		messageType, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			<-pumpDone
			if ctx.Err() != nil || websocket.IsCloseError(readErr, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return nil
		}
		if messageType != websocket.TextMessage {
			continue
		}
		var parsed deepgramResponse
		if err := json.Unmarshal(payload, &parsed); err != nil {
			continue
		}
		if parsed.Type != "Results" || len(parsed.Channel.Alternatives) == 0 {
			continue
		}
		best := parsed.Channel.Alternatives[0]
		transcript := strings.TrimSpace(best.Transcript)
		if transcript == "" {
			continue
		}
		if onResult != nil {
			onResult(TranscriptionResult{
				Text:       transcript,
				Confidence: best.Confidence,
				IsFinal:    parsed.IsFinal,
			})
		}
	}
}

// Close satisfies STTProvider.
func (provider *DeepgramSTT) Close() error { return nil }

// OpenAISTT buffers mono PCM audio into a WAV container and transcribes it via
// OpenAI's Whisper transcription endpoint.
type OpenAISTT struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Transcribe collects audio until the channel closes, wraps it in WAV, and calls Whisper.
func (provider *OpenAISTT) Transcribe(
	ctx context.Context,
	audio <-chan []byte,
	sampleRate int,
	onResult func(TranscriptionResult),
) error {
	if sampleRate <= 0 {
		sampleRate = 48000
	}
	var pcm bytes.Buffer
	for chunk := range audio {
		pcm.Write(chunk)
	}
	if pcm.Len() == 0 {
		return nil
	}

	var wavBuf bytes.Buffer
	if err := WriteWAV(&wavBuf, pcm.Bytes(), sampleRate, 1); err != nil {
		return err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", "whisper-1"); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("file", "audio.wav")
	if err != nil {
		return err
	}
	if _, err := part.Write(wavBuf.Bytes()); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/audio/transcriptions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	client := provider.HTTPClient
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("openai STT request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("openai STT error (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse openai STT response: %w", err)
	}
	if text := strings.TrimSpace(parsed.Text); text != "" && onResult != nil {
		onResult(TranscriptionResult{
			Text:       text,
			Confidence: 1.0,
			IsFinal:    true,
		})
	}
	return nil
}

// Close satisfies STTProvider.
func (provider *OpenAISTT) Close() error { return nil }
