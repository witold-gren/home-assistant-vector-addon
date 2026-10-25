# Changelog

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
