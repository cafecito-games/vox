package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/voice"
	"github.com/disgoorg/snowflake/v2"
	"github.com/pion/opus"
)

// DefaultConfig returns the baseline voice engine configuration.
func DefaultConfig() map[string]any {
	return map[string]any{
		"tts_provider":        "elevenlabs",
		"tts_voice":           "default",
		"stt_provider":        "deepgram",
		"vad_threshold":       0.5,
		"silence_duration_ms": 800,
		"playback_volume":     1.0,
	}
}

type playbackJob struct {
	source     string
	isURL      bool
	pcm        []byte
	sampleRate int
	channels   int
}

// AudioPlayer queues FFmpeg-transcoded Opus frames onto a disgo voice.Conn.
type AudioPlayer struct {
	conn   voice.Conn
	logger *slog.Logger

	mutex      sync.Mutex
	volume     float64
	playing    atomic.Bool
	paused     atomic.Bool
	closed     atomic.Bool
	queue      chan playbackJob
	stopSignal chan struct{}
	frames     chan []byte
	cancelPlay context.CancelFunc
	done       chan struct{}
}

// NewAudioPlayer constructs and starts a queue-based AudioPlayer bound to conn.
func NewAudioPlayer(conn voice.Conn, volume float64, logger *slog.Logger) *AudioPlayer {
	if logger == nil {
		logger = slog.Default()
	}
	player := &AudioPlayer{
		conn:       conn,
		logger:     logger,
		volume:     clampVolume(volume),
		queue:      make(chan playbackJob, 32),
		stopSignal: make(chan struct{}, 1),
		frames:     make(chan []byte, 64),
		done:       make(chan struct{}),
	}
	conn.SetOpusFrameProvider(player)
	go player.run()
	return player
}

func clampVolume(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 2.0 {
		return 2.0
	}
	return v
}

// ProvideOpusFrame implements disgo's voice.OpusFrameProvider.
func (player *AudioPlayer) ProvideOpusFrame() ([]byte, error) {
	if player.closed.Load() {
		return nil, io.EOF
	}
	for player.paused.Load() {
		if player.closed.Load() {
			return nil, io.EOF
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case frame, ok := <-player.frames:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-player.done:
		return nil, io.EOF
	}
}

// EnqueueFile schedules a local audio file for playback via FFmpeg.
func (player *AudioPlayer) EnqueueFile(path string) error {
	if player.closed.Load() {
		return errors.New("audio player is closed")
	}
	player.queue <- playbackJob{source: path}
	return nil
}

// EnqueueURL schedules an HTTP(S) audio stream for playback via FFmpeg.
func (player *AudioPlayer) EnqueueURL(streamURL string) error {
	if player.closed.Load() {
		return errors.New("audio player is closed")
	}
	player.queue <- playbackJob{source: streamURL, isURL: true}
	return nil
}

// EnqueuePCM schedules headerless signed 16-bit PCM bytes (24 kHz mono from TTS).
func (player *AudioPlayer) EnqueuePCM(pcm []byte, sampleRate, channels int) error {
	if player.closed.Load() {
		return errors.New("audio player is closed")
	}
	if sampleRate <= 0 {
		sampleRate = 24000
	}
	if channels <= 0 {
		channels = 1
	}
	player.queue <- playbackJob{pcm: pcm, sampleRate: sampleRate, channels: channels}
	return nil
}

// SetVolume updates playback volume clamped to [0.0, 2.0].
func (player *AudioPlayer) SetVolume(volume float64) {
	player.mutex.Lock()
	player.volume = clampVolume(volume)
	player.mutex.Unlock()
}

// Pause pauses audio transmission without disconnecting or stopping listening.
func (player *AudioPlayer) Pause() { player.paused.Store(true) }

// Resume resumes paused playback.
func (player *AudioPlayer) Resume() { player.paused.Store(false) }

// Stop aborts the currently playing item without closing the player loop.
func (player *AudioPlayer) Stop() {
	player.paused.Store(false)
	player.mutex.Lock()
	if player.cancelPlay != nil {
		player.cancelPlay()
	}
	player.mutex.Unlock()
}

// IsPlaying reports whether audio is actively being streamed.
func (player *AudioPlayer) IsPlaying() bool { return player.playing.Load() }

// IsPaused reports whether playback is currently paused.
func (player *AudioPlayer) IsPaused() bool { return player.paused.Load() }

// Close implements voice.OpusFrameProvider and shuts down the playback loop.
func (player *AudioPlayer) Close() {
	if player.closed.Swap(true) {
		return
	}
	player.Stop()
	close(player.done)
	close(player.queue)
}

func (player *AudioPlayer) run() {
	defer close(player.frames)
	for job := range player.queue {
		if player.closed.Load() {
			return
		}
		player.playJob(job)
	}
}

func (player *AudioPlayer) playJob(job playbackJob) {
	ctx, cancel := context.WithCancel(context.Background())
	player.mutex.Lock()
	player.cancelPlay = cancel
	vol := player.volume
	player.mutex.Unlock()

	defer func() {
		cancel()
		player.playing.Store(false)
		player.mutex.Lock()
		player.cancelPlay = nil
		player.mutex.Unlock()
	}()

	args := []string{"-hide_banner", "-loglevel", "error"}
	if len(job.pcm) > 0 {
		args = append(args,
			"-f", "s16le",
			"-ar", strconv.Itoa(job.sampleRate),
			"-ac", strconv.Itoa(job.channels),
			"-i", "pipe:0",
		)
	} else {
		if job.isURL {
			args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5")
		}
		args = append(args, "-i", job.source)
	}
	if vol != 1.0 {
		args = append(args, "-filter:a", fmt.Sprintf("volume=%.3f", vol))
	}
	args = append(args,
		"-c:a", "libopus",
		"-ar", "48000",
		"-ac", "2",
		"-frame_duration", "20",
		"-f", "ogg",
		"pipe:1",
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if len(job.pcm) > 0 {
		cmd.Stdin = bytes.NewReader(job.pcm)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		player.logger.Warn("ffmpeg stdout pipe error", "error", err)
		return
	}
	if err := cmd.Start(); err != nil {
		player.logger.Warn("ffmpeg start error", "error", err)
		return
	}

	_ = player.conn.SetSpeaking(ctx, voice.SpeakingFlagMicrophone)
	player.playing.Store(true)

	demux := NewOggOpusReader(stdout)
playLoop:
	for {
		packet, readErr := demux.NextPacket()
		if readErr != nil {
			break
		}
		select {
		case player.frames <- packet:
		case <-ctx.Done():
			break playLoop
		case <-player.done:
			break playLoop
		}
	}

	_ = cmd.Wait()
	_ = player.conn.SetSpeaking(context.Background(), voice.SpeakingFlagNone)
}

// AudioListener decodes incoming Opus frames per speaker in pure Go via
// pion/opus and streams mono PCM into the active STT provider.
type AudioListener struct {
	conn           voice.Conn
	stt            STTProvider
	logger         *slog.Logger
	onTranscript   func(userID string, result TranscriptionResult)
	mutex          sync.Mutex
	decoders       map[snowflake.ID]*opus.Decoder
	speakerQueues  map[snowflake.ID]chan []byte
	speakerCancels map[snowflake.ID]context.CancelFunc
	closed         atomic.Bool
}

// NewAudioListener attaches an OpusFrameReceiver to conn and streams transcripts.
func NewAudioListener(
	conn voice.Conn,
	stt STTProvider,
	logger *slog.Logger,
	onTranscript func(userID string, result TranscriptionResult),
) *AudioListener {
	if logger == nil {
		logger = slog.Default()
	}
	listener := &AudioListener{
		conn:           conn,
		stt:            stt,
		logger:         logger,
		onTranscript:   onTranscript,
		decoders:       make(map[snowflake.ID]*opus.Decoder),
		speakerQueues:  make(map[snowflake.ID]chan []byte),
		speakerCancels: make(map[snowflake.ID]context.CancelFunc),
	}
	conn.SetOpusFrameReceiver(listener)
	return listener
}

// ReceiveOpusFrame implements disgo's voice.OpusFrameReceiver.
func (listener *AudioListener) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if listener.closed.Load() || packet == nil || len(packet.Opus) == 0 {
		return nil
	}

	listener.mutex.Lock()
	decoder, ok := listener.decoders[userID]
	if !ok {
		dec, err := opus.NewDecoderWithOutput(48000, 2)
		if err != nil {
			listener.mutex.Unlock()
			listener.logger.Debug("opus decoder init failed", "user_id", userID, "error", err)
			return nil //nolint:nilerr // drop unreadable speaker stream without killing voice receiver
		}
		decoder = &dec
		listener.decoders[userID] = decoder
	}
	queue, hasQueue := listener.speakerQueues[userID]
	if !hasQueue {
		queue = make(chan []byte, 200)
		ctx, cancel := context.WithCancel(context.Background())
		listener.speakerQueues[userID] = queue
		listener.speakerCancels[userID] = cancel
		go func() {
			defer cancel()
			listener.runSpeakerSession(ctx, userID, queue)
		}()
	}
	listener.mutex.Unlock()

	pcmOut := make([]int16, 960*2*6) // up to 120ms stereo @ 48 kHz
	samplesPerChannel, err := decoder.DecodeToInt16(packet.Opus, pcmOut)
	if err != nil || samplesPerChannel <= 0 {
		// Swallow malformed packets so one bad frame never tears down listening.
		return nil //nolint:nilerr // intentionally swallow single corrupted opus frame
	}
	mono := StereoInt16ToMonoBytes(pcmOut[:samplesPerChannel*2])

	select {
	case queue <- mono:
	default:
		// Drop oldest frame under backpressure.
		select {
		case <-queue:
		default:
		}
		select {
		case queue <- mono:
		default:
		}
	}
	return nil
}

func (listener *AudioListener) runSpeakerSession(ctx context.Context, userID snowflake.ID, queue <-chan []byte) {
	err := listener.stt.Transcribe(ctx, queue, 48000, func(result TranscriptionResult) {
		result.UserID = userID.String()
		if listener.onTranscript != nil {
			listener.onTranscript(userID.String(), result)
		}
	})
	if err != nil && ctx.Err() == nil {
		listener.logger.Warn("STT speaker session ended with error", "user_id", userID, "error", err)
	}
}

// CleanupUser tears down decoder and STT state when a speaker disconnects.
func (listener *AudioListener) CleanupUser(userID snowflake.ID) {
	listener.mutex.Lock()
	defer listener.mutex.Unlock()
	if cancel, ok := listener.speakerCancels[userID]; ok {
		cancel()
		delete(listener.speakerCancels, userID)
	}
	if queue, ok := listener.speakerQueues[userID]; ok {
		close(queue)
		delete(listener.speakerQueues, userID)
	}
	delete(listener.decoders, userID)
}

// Close implements voice.OpusFrameReceiver and cleanly shuts down all STT sessions.
func (listener *AudioListener) Close() {
	if listener.closed.Swap(true) {
		return
	}
	listener.mutex.Lock()
	defer listener.mutex.Unlock()
	for uid, cancel := range listener.speakerCancels {
		cancel()
		delete(listener.speakerCancels, uid)
	}
	for uid, queue := range listener.speakerQueues {
		close(queue)
		delete(listener.speakerQueues, uid)
	}
	clear(listener.decoders)
}

// CapturedSpeaker describes one WAV file written by `vox voice capture`.
type CapturedSpeaker struct {
	UserID     string `json:"user_id"`
	Packets    int    `json:"packets"`
	Bytes      int    `json:"bytes"`
	DurationMS int    `json:"duration_ms"`
	Path       string `json:"path"`
}

// CaptureReceiver implements voice.OpusFrameReceiver to write raw 48 kHz stereo
// PCM WAV files per speaker for debugging voice receive independent of STT.
type CaptureReceiver struct {
	mutex    sync.Mutex
	decoders map[snowflake.ID]*opus.Decoder
	buffers  map[snowflake.ID]*bytes.Buffer
	packets  map[snowflake.ID]int
	closed   atomic.Bool
}

// NewCaptureReceiver creates a CaptureReceiver ready to attach to a voice.Conn.
func NewCaptureReceiver() *CaptureReceiver {
	return &CaptureReceiver{
		decoders: make(map[snowflake.ID]*opus.Decoder),
		buffers:  make(map[snowflake.ID]*bytes.Buffer),
		packets:  make(map[snowflake.ID]int),
	}
}

// ReceiveOpusFrame decodes Opus audio into 48 kHz stereo s16le PCM and buffers it per speaker.
func (receiver *CaptureReceiver) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
	if receiver.closed.Load() || packet == nil || len(packet.Opus) == 0 {
		return nil
	}
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()

	decoder, ok := receiver.decoders[userID]
	if !ok {
		dec, err := opus.NewDecoderWithOutput(48000, 2)
		if err != nil {
			return nil
		}
		decoder = &dec
		receiver.decoders[userID] = decoder
	}
	pcmOut := make([]int16, 960*2*6)
	samples, err := decoder.DecodeToInt16(packet.Opus, pcmOut)
	if err != nil || samples <= 0 {
		return nil
	}
	buf, ok := receiver.buffers[userID]
	if !ok {
		buf = &bytes.Buffer{}
		receiver.buffers[userID] = buf
	}
	buf.Write(Int16ToLittleEndianBytes(pcmOut[:samples*2]))
	receiver.packets[userID]++
	return nil
}

// CleanupUser satisfies voice.OpusFrameReceiver.
func (receiver *CaptureReceiver) CleanupUser(_ snowflake.ID) {}

// Close satisfies voice.OpusFrameReceiver.
func (receiver *CaptureReceiver) Close() { receiver.closed.Store(true) }

// WriteWAVFiles flushes all captured speaker buffers to outputDir as WAV files.
func (receiver *CaptureReceiver) WriteWAVFiles(outputDir string) ([]CapturedSpeaker, error) {
	if err := os.MkdirAll(outputDir, 0o700); err != nil {
		return nil, err
	}
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()

	results := make([]CapturedSpeaker, 0, len(receiver.buffers))
	for userID, buf := range receiver.buffers {
		pcm := buf.Bytes()
		filePath := filepath.Join(outputDir, fmt.Sprintf("%s.wav", userID.String()))
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		writeErr := WriteWAV(file, pcm, 48000, 2)
		closeErr := file.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		// 48000 Hz * 2 channels * 2 bytes = 192000 bytes/sec = 192 bytes/ms.
		durationMS := len(pcm) / 192
		results = append(results, CapturedSpeaker{
			UserID:     userID.String(),
			Packets:    receiver.packets[userID],
			Bytes:      len(pcm),
			DurationMS: durationMS,
			Path:       filePath,
		})
	}
	return results, nil
}

// ConnectionStatus summarizes one active voice connection.
type ConnectionStatus struct {
	GuildID     string `json:"guild_id"`
	ChannelID   string `json:"channel_id"`
	IsPlaying   bool   `json:"is_playing"`
	IsPaused    bool   `json:"is_paused"`
	IsListening bool   `json:"is_listening"`
}

// Engine manages voice connections, audio players, and STT listeners across guilds.
type Engine struct {
	client *bot.Client
	logger *slog.Logger

	mutex        sync.Mutex
	config       map[string]any
	conns        map[snowflake.ID]voice.Conn
	players      map[snowflake.ID]*AudioPlayer
	listeners    map[snowflake.ID]*AudioListener
	tts          TTSProvider
	stt          STTProvider
	eventHandler func(map[string]any)
}

// NewEngine creates a VoiceEngine bound to a Gateway bot.Client.
func NewEngine(client *bot.Client, initialConfig map[string]any, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	cfg := DefaultConfig()
	maps.Copy(cfg, initialConfig)
	return &Engine{
		client:    client,
		logger:    logger,
		config:    cfg,
		conns:     make(map[snowflake.ID]voice.Conn),
		players:   make(map[snowflake.ID]*AudioPlayer),
		listeners: make(map[snowflake.ID]*AudioListener),
	}
}

// SetEventHandler sets the callback invoked on voice lifecycle and transcript events.
func (engine *Engine) SetEventHandler(handler func(map[string]any)) {
	engine.mutex.Lock()
	engine.eventHandler = handler
	engine.mutex.Unlock()
}

func (engine *Engine) emit(event map[string]any) {
	engine.mutex.Lock()
	handler := engine.eventHandler
	engine.mutex.Unlock()
	if handler != nil {
		handler(event)
	}
}

// Config returns a shallow clone of the current voice engine configuration.
func (engine *Engine) Config() map[string]any {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	return maps.Clone(engine.config)
}

// UpdateConfig merges updates into the voice engine configuration and resets
// cached providers when their provider names change.
func (engine *Engine) UpdateConfig(updates map[string]any) map[string]any {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	if nextTTS, ok := updates["tts_provider"].(string); ok && nextTTS != engine.config["tts_provider"] {
		if engine.tts != nil {
			_ = engine.tts.Close()
			engine.tts = nil
		}
	}
	if nextSTT, ok := updates["stt_provider"].(string); ok && nextSTT != engine.config["stt_provider"] {
		if engine.stt != nil {
			_ = engine.stt.Close()
			engine.stt = nil
		}
	}
	maps.Copy(engine.config, updates)
	if vol, ok := updates["playback_volume"].(float64); ok {
		for _, player := range engine.players {
			player.SetVolume(vol)
		}
	}
	return maps.Clone(engine.config)
}

// Connect joins a voice channel (or moves an existing guild connection) and starts an AudioPlayer.
func (engine *Engine) Connect(ctx context.Context, guildID, channelID snowflake.ID) (voice.Conn, error) {
	conn := engine.client.VoiceManager.CreateConn(guildID)
	if err := conn.Open(ctx, channelID, false, false); err != nil {
		return nil, fmt.Errorf("open voice connection: %w", err)
	}

	engine.mutex.Lock()
	if oldPlayer, ok := engine.players[guildID]; ok {
		oldPlayer.Close()
	}
	vol, _ := engine.config["playback_volume"].(float64)
	if vol <= 0 {
		vol = 1.0
	}
	engine.conns[guildID] = conn
	engine.players[guildID] = NewAudioPlayer(conn, vol, engine.logger)
	engine.mutex.Unlock()

	engine.emit(map[string]any{
		"event":      "voice_connected",
		"guild_id":   guildID.String(),
		"channel_id": channelID.String(),
	})
	return conn, nil
}

// Disconnect leaves the active voice channel in guildID and tears down playback/listening.
func (engine *Engine) Disconnect(ctx context.Context, guildID snowflake.ID) error {
	engine.mutex.Lock()
	if listener, ok := engine.listeners[guildID]; ok {
		listener.Close()
		delete(engine.listeners, guildID)
	}
	if player, ok := engine.players[guildID]; ok {
		player.Close()
		delete(engine.players, guildID)
	}
	conn, existed := engine.conns[guildID]
	delete(engine.conns, guildID)
	engine.mutex.Unlock()

	if existed && conn != nil {
		conn.Close(ctx)
	}
	engine.client.VoiceManager.RemoveConn(guildID)

	engine.emit(map[string]any{
		"event":    "voice_disconnected",
		"guild_id": guildID.String(),
	})
	return nil
}

// Move moves the bot's active voice connection in guildID to channelID.
func (engine *Engine) Move(ctx context.Context, guildID, channelID snowflake.ID) error {
	_, err := engine.Connect(ctx, guildID, channelID)
	return err
}

// Speak synthesizes text via the configured TTS provider and plays it in guildID.
func (engine *Engine) Speak(ctx context.Context, guildID snowflake.ID, text, voiceName string, speed float64) error {
	engine.mutex.Lock()
	player, ok := engine.players[guildID]
	if !ok {
		engine.mutex.Unlock()
		return errors.New("not connected to voice in this server")
	}
	if engine.tts == nil {
		providerName, _ := engine.config["tts_provider"].(string)
		tts, err := NewTTSProvider(providerName)
		if err != nil {
			engine.mutex.Unlock()
			return err
		}
		engine.tts = tts
	}
	tts := engine.tts
	if voiceName == "" {
		voiceName, _ = engine.config["tts_voice"].(string)
	}
	engine.mutex.Unlock()

	pcm, err := tts.Synthesize(ctx, text, voiceName, speed)
	if err != nil {
		return err
	}
	if err := player.EnqueuePCM(pcm, 24000, 1); err != nil {
		return err
	}
	engine.emit(map[string]any{
		"event":    "voice_playback_started",
		"guild_id": guildID.String(),
		"type":     "tts",
	})
	return nil
}

// Play enqueues a local file or HTTP(S) URL for playback in guildID.
func (engine *Engine) Play(guildID snowflake.ID, source string) error {
	engine.mutex.Lock()
	player, ok := engine.players[guildID]
	engine.mutex.Unlock()
	if !ok {
		return errors.New("not connected to voice in this server")
	}

	var err error
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		err = player.EnqueueURL(source)
	} else {
		err = player.EnqueueFile(source)
	}
	if err != nil {
		return err
	}
	engine.emit(map[string]any{
		"event":    "voice_playback_started",
		"guild_id": guildID.String(),
		"type":     "file",
		"source":   source,
	})
	return nil
}

// Stop stops playback in guildID.
func (engine *Engine) Stop(guildID snowflake.ID) error {
	engine.mutex.Lock()
	player, ok := engine.players[guildID]
	engine.mutex.Unlock()
	if !ok {
		return errors.New("not connected to voice in this server")
	}
	player.Stop()
	engine.emit(map[string]any{
		"event":    "voice_playback_finished",
		"guild_id": guildID.String(),
	})
	return nil
}

// Pause pauses playback in guildID.
func (engine *Engine) Pause(guildID snowflake.ID) error {
	engine.mutex.Lock()
	player, ok := engine.players[guildID]
	engine.mutex.Unlock()
	if !ok {
		return errors.New("not connected to voice in this server")
	}
	player.Pause()
	return nil
}

// Resume resumes playback in guildID.
func (engine *Engine) Resume(guildID snowflake.ID) error {
	engine.mutex.Lock()
	player, ok := engine.players[guildID]
	engine.mutex.Unlock()
	if !ok {
		return errors.New("not connected to voice in this server")
	}
	player.Resume()
	return nil
}

// ListenStart begins streaming per-speaker audio in guildID to the STT provider.
func (engine *Engine) ListenStart(guildID snowflake.ID) error {
	engine.mutex.Lock()
	if _, already := engine.listeners[guildID]; already {
		engine.mutex.Unlock()
		return nil
	}
	conn, ok := engine.conns[guildID]
	if !ok {
		engine.mutex.Unlock()
		return errors.New("not connected to voice in this server")
	}
	if engine.stt == nil {
		providerName, _ := engine.config["stt_provider"].(string)
		stt, err := NewSTTProvider(providerName)
		if err != nil {
			engine.mutex.Unlock()
			return err
		}
		engine.stt = stt
	}
	stt := engine.stt
	listener := NewAudioListener(conn, stt, engine.logger, func(userID string, result TranscriptionResult) {
		if !result.IsFinal {
			return
		}
		channelID := ""
		if ch := conn.ChannelID(); ch != nil {
			channelID = ch.String()
		}
		engine.emit(map[string]any{
			"event":      "voice_speech_detected",
			"guild_id":   guildID.String(),
			"channel_id": channelID,
			"user_id":    userID,
			"text":       result.Text,
			"confidence": result.Confidence,
			"is_final":   true,
		})
	})
	engine.listeners[guildID] = listener
	engine.mutex.Unlock()
	return nil
}

// ListenStop stops active STT listening in guildID.
func (engine *Engine) ListenStop(guildID snowflake.ID) {
	engine.mutex.Lock()
	listener, ok := engine.listeners[guildID]
	delete(engine.listeners, guildID)
	engine.mutex.Unlock()
	if ok && listener != nil {
		listener.Close()
	}
}

// Status returns the status of every active voice connection.
func (engine *Engine) Status() []ConnectionStatus {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	out := make([]ConnectionStatus, 0, len(engine.conns))
	for guildID, conn := range engine.conns {
		chID := ""
		if ch := conn.ChannelID(); ch != nil {
			chID = ch.String()
		}
		player := engine.players[guildID]
		_, listening := engine.listeners[guildID]
		out = append(out, ConnectionStatus{
			GuildID:     guildID.String(),
			ChannelID:   chID,
			IsPlaying:   player != nil && player.IsPlaying(),
			IsPaused:    player != nil && player.IsPaused(),
			IsListening: listening,
		})
	}
	return out
}

// Close disconnects every voice connection and releases TTS/STT providers.
func (engine *Engine) Close(ctx context.Context) {
	engine.mutex.Lock()
	guildIDs := make([]snowflake.ID, 0, len(engine.conns))
	for guildID := range engine.conns {
		guildIDs = append(guildIDs, guildID)
	}
	tts := engine.tts
	stt := engine.stt
	engine.tts = nil
	engine.stt = nil
	engine.mutex.Unlock()

	for _, guildID := range guildIDs {
		_ = engine.Disconnect(ctx, guildID)
	}
	if tts != nil {
		_ = tts.Close()
	}
	if stt != nil {
		_ = stt.Close()
	}
}
