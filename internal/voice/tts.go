// Package voice implements Discord voice connectivity (with pure-Go DAVE E2EE
// via dave-go and disgo), FFmpeg-backed audio playback, pure-Go Opus decoding
// via pion/opus, WAV capture, and pluggable TTS/STT providers.
package voice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// TTSProvider synthesizes text into 24 kHz mono signed 16-bit little-endian PCM.
type TTSProvider interface {
	Synthesize(ctx context.Context, text string, voiceName string, speed float64) ([]byte, error)
	Close() error
}

// NewTTSProvider instantiates a TTS provider by name ("elevenlabs", "openai", "deepgram").
func NewTTSProvider(name string) (TTSProvider, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "elevenlabs":
		key, err := requireEnv("ELEVENLABS_API_KEY")
		if err != nil {
			return nil, err
		}
		return &ElevenLabsTTS{APIKey: key, HTTPClient: defaultHTTPClient()}, nil
	case "openai":
		key, err := requireEnv("OPENAI_API_KEY")
		if err != nil {
			return nil, err
		}
		return &OpenAITTS{APIKey: key, HTTPClient: defaultHTTPClient()}, nil
	case "deepgram":
		key, err := requireEnv("DEEPGRAM_API_KEY")
		if err != nil {
			return nil, err
		}
		return &DeepgramTTS{APIKey: key, HTTPClient: defaultHTTPClient()}, nil
	default:
		return nil, fmt.Errorf("unknown TTS provider %q (supported: elevenlabs, openai, deepgram)", name)
	}
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 60 * time.Second}
}

func requireEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", fmt.Errorf("missing required environment variable %s", name)
	}
	return value, nil
}

// ElevenLabsTTS calls ElevenLabs' streaming PCM 24 kHz text-to-speech API.
type ElevenLabsTTS struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// DefaultElevenLabsVoice is used when callers request "default".
const DefaultElevenLabsVoice = "21m00Tcm4TlvDq8ikWAM"

// Synthesize renders text to 24 kHz mono s16le PCM using ElevenLabs.
func (provider *ElevenLabsTTS) Synthesize(ctx context.Context, text, voiceName string, _ float64) ([]byte, error) {
	voiceID := strings.TrimSpace(voiceName)
	if voiceID == "" || strings.EqualFold(voiceID, "default") {
		voiceID = DefaultElevenLabsVoice
	}
	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.elevenlabs.io"
	}
	endpoint := fmt.Sprintf("%s/v1/text-to-speech/%s/stream?output_format=pcm_24000", strings.TrimRight(baseURL, "/"), url.PathEscape(voiceID))

	body, err := json.Marshal(map[string]any{
		"text":     text,
		"model_id": "eleven_turbo_v2_5",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	return doTTSRequest(provider.HTTPClient, req, "elevenlabs")
}

// Close satisfies TTSProvider.
func (provider *ElevenLabsTTS) Close() error { return nil }

// OpenAITTS calls OpenAI's audio/speech endpoint requesting raw 24 kHz PCM.
type OpenAITTS struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// Synthesize renders text to 24 kHz mono s16le PCM using OpenAI TTS.
func (provider *OpenAITTS) Synthesize(ctx context.Context, text, voiceName string, speed float64) ([]byte, error) {
	voiceID := strings.TrimSpace(voiceName)
	if voiceID == "" || strings.EqualFold(voiceID, "default") {
		voiceID = "alloy"
	}
	if speed <= 0 {
		speed = 1.0
	}
	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/audio/speech"

	body, err := json.Marshal(map[string]any{
		"model":           "tts-1",
		"voice":           voiceID,
		"input":           text,
		"response_format": "pcm",
		"speed":           speed,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	return doTTSRequest(provider.HTTPClient, req, "openai")
}

// Close satisfies TTSProvider.
func (provider *OpenAITTS) Close() error { return nil }

// DeepgramTTS calls Deepgram Aura's linear16 24 kHz speak API.
type DeepgramTTS struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// DefaultDeepgramAuraModel is Deepgram's default Aura TTS voice model.
const DefaultDeepgramAuraModel = "aura-2-asteria-en"

// Synthesize renders text to 24 kHz mono s16le PCM using Deepgram Aura.
func (provider *DeepgramTTS) Synthesize(ctx context.Context, text, voiceName string, _ float64) ([]byte, error) {
	model := strings.TrimSpace(voiceName)
	if model == "" || strings.EqualFold(model, "default") || !strings.HasPrefix(model, "aura") {
		model = DefaultDeepgramAuraModel
	}
	baseURL := provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.deepgram.com"
	}
	params := url.Values{}
	params.Set("model", model)
	params.Set("encoding", "linear16")
	params.Set("sample_rate", "24000")
	endpoint := strings.TrimRight(baseURL, "/") + "/v1/speak?" + params.Encode()

	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Token "+provider.APIKey)
	req.Header.Set("Content-Type", "application/json")

	return doTTSRequest(provider.HTTPClient, req, "deepgram")
}

// Close satisfies TTSProvider.
func (provider *DeepgramTTS) Close() error { return nil }

func doTTSRequest(client *http.Client, req *http.Request, name string) ([]byte, error) {
	if client == nil {
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req) //nolint:gosec // request targets configured TTS provider API endpoint
	if err != nil {
		return nil, fmt.Errorf("%s TTS request failed: %w", name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s TTS response: %w", name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s TTS error (%d): %s", name, resp.StatusCode, strings.TrimSpace(string(payload)))
	}
	if len(payload) == 0 {
		return nil, errors.New("TTS provider returned empty audio payload")
	}
	return payload, nil
}
