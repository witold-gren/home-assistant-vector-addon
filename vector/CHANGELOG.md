# Changelog

## 0.3.0
- **Program sessions for Robots Code Studio** (children's block / Python programs run step by step):
  `program_begin {owner, limit, heartbeat, drive_off_charger}` → `{session, …}`, `program_heartbeat`,
  `program_end`; events `program_started` / `program_ended {reason, cause}`; state and hello field
  `program` (`{owner, since, limit}`), `hello.features: ["program"]`. During a program the app holds
  behavior control the whole time (no 2-second release, no 600-second hold limit), other clients'
  motion / animation / speech commands get `program running`, and a watchdog stops the robot when the
  program stops sending heartbeats. **`stop` from Home Assistant always ends the program**
  (`ha_stop`). Vector taking control back (cliff, picked up) ends it with `control_lost` and a cause.
- `sensors`: an instant reading (no waiting for the 1-second state push) — touch, picked up, held,
  **falling**, cliff, **distance sensor** (`obstacle_mm`, `obstacle_ok`), charger, battery, head, lift,
  pose, last face with its age, cube (connected, visible, taps), last sentence heard, accelerometer,
  gyroscope, wheel speeds, and counters since the program began.
- `set_wheels {left, right}`: continuous driving inside a program (stopped by the program's end, `stop`
  or the watchdog). `set_cube_lights {color | hue+saturation, mode on|flash|off}`.
- New option **`studio_token`**: the studio logs in with it and may only run programs and read the
  state (no settings, faces, photos, camera, microphone). Empty = no studio. To reach the app from the
  studio, publish port 3012 (DOCS: Robots Code Studio).
- During a program `POST /audio` from anybody but the program answers 409.
- FAQ: a program stopped by itself, the studio cannot connect, the card does nothing during a program.

## 0.2.3
- `stream_audio` is off by default: Vector's firmware sends a 1 kHz test tone through the SDK
  audio feed, not the microphone (`sdkComponent.cpp`: "temporary 1 kHz sine wave generator"), so
  the live view only beeped. To talk to Vector, use 🎙 on the card (integration 0.2.3).
- FAQ: "the live view only beeps".

## 0.2.2
- **Live view that starts reliably.** New command `warm_stream`: an internal ffmpeg viewer starts
  the go2rtc stream and holds it for 20 s, so Home Assistant's go2rtc (which waits only 5 s for the
  stream description) attaches to a running stream. Vector's camera on the charger needs longer
  than that (it takes behavior control first): the view failed and Home Assistant fell back to HLS,
  failing the same way. Two attempts within 45 s. Needs integration 0.2.2.
- Microphone sample rate defaults to 12000 Hz (measured 11.7–12.1 kHz on a Vector 2.0).
- FAQ: black full screen, live view that stops.

## 0.2.1
- go2rtc logs straight to the app log: the `[go2rtc]` prefix used `sed -u`, which BusyBox does not
  have (`sed: unrecognized option: u`), so go2rtc's output went into a broken pipe and its first
  warning would have stopped it.
- `init` removed from the configuration (it is the default; the add-on linter asks for it).

## 0.2.0
- **Live stream instead of snapshots.** The app runs go2rtc and serves the camera as real video (H.264)
  with Vector's microphone (Opus) over RTSP inside the Home Assistant network; Home Assistant's go2rtc
  turns it into WebRTC. It runs only while somebody watches, and the camera is closed cleanly when the
  last viewer leaves. Options `live_stream`, `stream_audio`, `mic_sample_rate`.
- The camera's ~2 pictures per second are stamped with the real clock and repeated at 10 fps: a
  steady stream that starts in about 2–4 s.
- New bridge endpoint `GET /mic` (the robot's microphone as 16-bit PCM, silence filled in) and a
  `stream` entry in `hello`, so the integration knows the stream exists.
- Fixed `FROM` and no `build.yaml`: Supervisor 2026.04+ no longer passes `BUILD_FROM`, so the build
  could fall back to the wrong base image.
- `init: true`: tini forwards SIGTERM to the bridge and reaps go2rtc/ffmpeg processes.
- FAQ (camera as a slideshow, broken image when the robot is off, no sound).

## 0.1.0
First public release.

- Connects to an Anki Vector through its SDK (gRPC); the robot's address and SDK key come from wire-pod.
- State and events: battery, charger, picked up, held, touch, cliff, sleeping, listening, activity,
  stimulation, faces (last seen, known faces), pose, cube, lifetime statistics, settings; events for
  wake word, touch, pick-up, cliff, back button, face seen, cube, photo, audio and lost control.
- Commands: speech (through wire-pod's TTS or Vector's own voice), animations and animation triggers,
  behaviors (app intents), drive on/off the charger, driving and turning, head and lift, hold-to-drive
  `move` with a dead man's switch, stop, eye colour, settings (volume, eye colour, language, time zone,
  location, units, 24-hour clock, back button), faces (list, rename, forget, meet), cube (connect,
  disconnect, flash, pick up, roll, dock, go to), go to a point, turn towards a face.
- Behavior control taken only while needed and given back 2 s after the last command; manual hold up
  to 10 minutes.
- Camera: MJPEG only while watched (taking control on the charger, where the camera needs it), cached
  snapshots that never wake the camera, a stuck camera is detected; photos stored on the robot.
- Speaker: any audio file (ffmpeg → 16 kHz PCM) played on the robot without behavior control, queue,
  announcements interrupt.
- Voice → Home Assistant: manages a wire-pod system intent `ha_router` that hands every sentence to the
  integration first (port 8790, token); wire-pod's LLM can be pointed at Home Assistant too (its settings
  are saved and restored).
- The bridge is compiled from source while the app is installed (amd64, aarch64).
