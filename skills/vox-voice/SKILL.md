---
name: vox-voice
description: Use Vox for Discord voice channel lookup, playback, text-to-speech, live transcription, and debugging audio capture.
---

# Vox voice

Resolve the voice channel and guild by ID when names repeat. Check the bot's server permissions with `vox doctor --server "$guild_id" --json`. Check channel overwrites for View Channel, Connect, and Speak as applicable. Vox includes Discord DAVE encryption support. Playback and text-to-speech require FFmpeg.

```sh
vox voice where "$user_id" --server "$guild_id" --json
vox voice members "$voice_channel_id" --server "$guild_id" --json
vox voice join "$voice_channel_id" --server "$guild_id" --json
```

Use `voice join` as a connection check. One-shot voice commands run their own sessions and release them on exit. For playback that must finish, status on existing connections, or pause/resume controls, use the same persistent `serve` process.

## Playback and persistent control

Start `vox serve --events voice` and wait for its `ready` event. Send JSONL over stdin. Read responses and events from stdout, match responses by `req_id`, and check the `error` field. Use string IDs.

```jsonl
{"action":"voice_connect","channel_id":"123456789012345678","req_id":"connect-1"}
{"action":"voice_play","guild_id":"234567890123456789","source":"./announcement.mp3","req_id":"play-1"}
```

Wait for connection success before playing. A local source path is resolved on the host running the daemon. Use `voice_playback_finished` to observe completion and `voice_status` to inspect the owning process. Use `voice_pause`, `voice_resume`, `voice_stop`, `voice_move`, and `voice_disconnect` in that same process. Discover their required IDs with `vox schema --json`.

For synthesis, send `voice_speak` with `guild_id` and `text`. The optional `tts` selects `elevenlabs`, `openai`, or `deepgram`. Inject the corresponding `ELEVENLABS_API_KEY`, `OPENAI_API_KEY`, or `DEEPGRAM_API_KEY` into the daemon environment. Use `voice_set_config` for session options such as `tts_voice` and `playback_volume`. Use `vox voice move <server> <member> <channel>` when the task is moving a Discord member.

## Transcription and capture

Confirm the channel, scope, and authorization before transcribing or capturing. Transcription sends audio to the configured speech-to-text provider. Deepgram and OpenAI are supported. The default STT provider is Deepgram.

```sh
vox voice listen --channel "$voice_channel_id" --duration 60 --json
```

One-shot transcription emits speaker events followed by a final summary. For a consistently line-delimited stream and live controls, use `voice_listen_start` in the connected daemon, consume `voice_speech_detected`, then send `voice_listen_stop` and disconnect. Select the STT provider with the action's `stt` field. For an authorized audio debug task, `voice capture --channel "$voice_channel_id" --duration 10 --output-dir ./captures --json` writes per-speaker WAV files without STT.
