# Findings from a real robot

What Vector's SDK (gRPC on the robot, port 443) really does — checked before and while writing the
bridge. Test robot: **Vector 2.0**, firmware `2.0.1.6076ep` (escape pod), engine `00001.00006.00000`;
wire-pod v1.2.18 with Wyoming (Whisper + Piper). October 2026.

Tool: `go run ./cmd/vector-probe -wirepod http://escapepod.local:8080 <creds|status|events|control|audio|camera|capture|photos|faces|cubes>`
(the GUID is never printed).

## 1. Credentials
- `GET /api-sdk/get_sdk_info` on wire-pod lists each robot with its own `guid` (24 characters); gRPC on
  `robot_ip:443` with TLS (self-signed, no verification) and `authorization: Bearer <guid>` works.
- vector-go-sdk (`fforchino`, 2023-11-08) pulls grpc 1.36 — upgraded to grpc 1.84 / protobuf 1.36, the
  `vectorpb` package compiles unchanged. Own dialer instead of `pkg/vector`.
- A flat or switched-off robot: no ping, port 443 closed → state `offline`, not an error.

## 2. Settings and statistics (PullJdocs)
ROBOT_SETTINGS: `master_volume` (0–5), `eye_color` (0–6), `custom_eye_color{enabled,hue,saturation}`, `locale`,
`time_zone`, `default_location`, `clock_24_hour`, `dist_is_metric`, `temp_is_fahrenheit`, `button_wakeword`.
ROBOT_LIFETIME_STATS: `Alive.seconds`, `BStat.ReactedToTriggerWord`, `FeatureType.Utility`, `Odom.Body`
(nanometres), `Pet.ms`, `Stim.CumlPosDelta`.
BatteryState: `battery_level` (enum), `battery_volts`, `is_charging`, `is_on_charger_platform` — no percentage.

## 3. EventStream (4 minutes of petting, lifting, "Hey Vector", a face, the charger)
Counts: RobotState 3991 (~17/s!), RobotObservedFace 282, StimulationInfo 247, KeepAlive 237 (~1/s),
empty 402, TimeStampedStatus 42, RobotChangedObservedFaceId 15, WakeWord 4, ConnectionResponse 1.
**No `UserIntent`** — intents handled by wire-pod never show up here, so voice → HA has to go through wire-pod.

- `robot_state.status` bits (confirmed): ON_CHARGER 0x1000, CHARGING 0x2000, PICKED_UP 0x8,
  BEING_HELD 0x10000, FALLING 0x20, CLIFF 0x4000, BUTTON_PRESSED 0x10, CALM_POWER_MODE 0x400,
  ANIMATING 0x40, MOTORS_MOVING 0x1, MOTION_DETECTED 0x20000 (noisy — not exposed).
- `robot_state.touch_data.is_being_touched` works (petting), but flickers → 1.5 s release delay.
- `wake_word` — `wake_word_begin` / `wake_word_end` (~4 s apart).
- `robot_observed_face` — `face_id`, `name`, `img_rect`, `pose`; very frequent → one event per new face id
  or per minute.
- `time_stamped_status.status.feature_status.feature_name` — **what Vector is doing now**, a good
  "activity" sensor: ObservingOnCharger, ListeningForBeats, Petting, Observing, ReactToCliff, StuckOnEdge,
  InTheAir, RobotShaken, Clock, InteractWithFaces, ReactToAffirmative, NoFeature (gaps — skipped).
- `stimulation_info.value` 0–1.

The bridge therefore pushes state at most once a second and only when something relevant changed.

## 4. Behavior control
- `ControlRequest{DEFAULT}` is granted after 0.5 s (awake robot) up to ~6 s (dozing on the charger).
- wire-pod does not take control back. After `ControlRelease` Vector goes back to his own behaviors.
- Actions (SetHeadAngle, …) need an `id_tag` from the SDK range (≥ 2000001), otherwise
  `InvalidArgument: Invalid Action tag_id`.
- SayText with Vector's voice works (English only, ~4.5 s per sentence) and needs control; wire-pod's
  `/api-sdk/say_text` speaks through wire-pod's TTS instead (e.g. Piper) without control.

## 5. Camera (CameraFeed)
- **On the charger, without control: 0 frames** — the bridge takes control while somebody watches.
  Off the charger frames come without control (first one after ~1.6 s).
- JPEG 800×600, ~26–30 kB per frame, **~2 fps** (`EnableImageStreaming` changes nothing). MJPEG is enough;
  a snapshot is the last cached frame. `CaptureSingleImage` takes 1.5–2 s (high resolution: no difference).
- The camera can get stuck (seen after a nap on the charger): 0 frames even with control, wire-pod's own
  camera view empty too — a robot restart helps; the bridge gives control back after two attempts.

## 6. Audio (ExternalAudioStreamPlayback)
- 16-bit mono PCM at 16 kHz in chunks of ≤1024 bytes, sent at real-time pace (≤1 s ahead):
  4.4 s of speech → `audio_stream_playback_complete` after 5.1 s. **Works without behavior control.**
  `audio_volume` 0–100. Any file is decoded with ffmpeg (`-ac 1 -ar 16000 -f s16le`).

## 7. wire-pod system intent (voice → HA)
`POST /api/add_custom_intent` with `"issystem": true`, `utterances: ["*"]`, `exec: /usr/bin/curl`
(present on the wire-pod server):
- Custom intents are checked **before** the built-in ones (`ProcessTextAll`: plugins → custom →
  keyphrases), so `*` sees every sentence. Speech recognition errors happen.
- Reply `{"status":"ok","returnIntent":"intent_system_noaudio"}` → Vector stays silent. Anything else →
  wire-pod carries on as usual (clock, dance, its LLM).
- exec + curl + hook take < 1 s. `exec` is synchronous — the hook must answer quickly.
- `execargs`: substitutions work for **whole** arguments only (`!speechText`, `!botSerial`, `!locale`,
  `!intentName`); `!speechText` arrives wrapped in literal quotes. The serial goes in as `-A !botSerial`
  (User-Agent). No shell → no quoting problems.
- `edit_custom_intent` clears `issystem` → change by remove + add (number = 1-based position).
- An empty transcript skips custom intents.
- wire-pod's web panel does not list system intents (`issystem: true`).

## 8. Robot settings
- gRPC `UpdateSettings` answers OK but the robot does not keep the change. The robot's REST gateway does
  (as wire-pod uses it): `POST https://<robot>/v1/update_settings {"update_settings": true, "settings": {...}}`.

## 9. Robot model and the face screen
- Vector 2.0: serial number starting with `0dd`, camera 800×600, screen 160×80.
- `DisplayFaceImageRGB` is **broken** on it: the SDK server still slices images as for Vector 1.0
  (184×96, chunks of 27 600 bytes) — a 160×80 image is rejected (`slice bounds out of range`), a 184×96
  one (and a 160×80 one padded to 184×96) crashes vic-anim (error 914/915, then 980 and a restart).
  Face-screen images are therefore not offered.
- `RequestEnrolledNames` (known faces) is safe.

## 10. Cube
- With a flat cube battery `CubesAvailable` returns an empty list at once and `ConnectCube` returns
  `success=false`.
