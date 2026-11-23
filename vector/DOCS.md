# Vector (Anki / Digital Dream Labs)

🇵🇱 [Polska wersja](DOCS.pl.md)

A bridge between the **Anki Vector** robot and the **Vector** integration for Home Assistant
([home-assistant-vector-integration](https://github.com/witold-gren/home-assistant-vector-integration)).
Vector keeps working with your [wire-pod](https://github.com/kercre123/wire-pod) server (speech
recognition, voice commands); the bridge talks to the robot directly through its SDK (gRPC on port 443)
and offers a WebSocket API to the integration.

## Requirements
- A Vector (1.0 or 2.0) set up with wire-pod and reachable from Home Assistant.
- The wire-pod web interface reachable from Home Assistant (default `http://escapepod.local:8080`; use
  the IP address if `.local` names do not resolve inside apps).
- The Vector integration (HACS or manual) — it is discovered automatically.

## How it works
- The robot's IP address and SDK key (GUID) are read from wire-pod (`/api-sdk/get_sdk_info`) on every
  (re)connection — nothing to copy by hand.
- Robot state (battery, charger, picked up, touch, cliff, sleeping, activity, faces, wake word, pose,
  cube) comes from the robot's event stream; it is pushed to Home Assistant at most once a second, while
  events (touch, face, "Hey Vector", photo, …) are pushed immediately.
- Motion, animations, Vector's own voice and the camera on the charger need **behavior control**. The
  bridge takes it only for the duration of a command and gives it back 2 s after the last one — then
  Vector goes back to his own behaviors. Manual control (a switch in the integration, or the control
  card) lasts at most 10 minutes.
- When the robot is off or flat, entities become unavailable and the bridge reconnects (1 s … 60 s).

## Speaker
The integration has a media player "Speaker": `tts.speak` with any engine (e.g. Piper), media files and
audio URLs play on Vector's speaker (16 kHz mono) without interrupting what he does. Clips queue up; an
announcement interrupts what is playing. Text can also be spoken through wire-pod (`vector.say_text`), so
with wire-pod's Wyoming TTS (e.g. Piper) Vector speaks any language.

## Camera, microphone and photos
- **Live stream** (option `live_stream`, on by default): the app runs [go2rtc](https://github.com/AlexxIT/go2rtc)
  and serves the camera as real video — H.264 — over RTSP inside the
  Home Assistant network (port 8554 of the app, not published on the host; user `vector`, the API token
  as the password). The integration gives this address to Home Assistant, whose own go2rtc turns it
  into WebRTC for the browser and the app. Nothing runs until somebody watches: go2rtc starts the camera
  and microphone feeds with the first viewer and stops them (SIGTERM, so the robot's camera is closed
  cleanly) a few seconds after the last one leaves.
- The robot's SDK camera gives about **2 pictures per second** (800×600). The stream repeats the last
  picture at 10 fps, so players start quickly and play smoothly; motion still updates about twice a
  second. The first picture appears about 2–4 s after you tap ▶ (longer on the charger, see below).
- Off the charger Vector keeps doing his things; **on the charger** frames only come with behavior
  control, so Vector holds still while you watch (and 10 s more).
- **No sound from Vector:** the SDK audio feed is a 1 kHz test tone in Vector's firmware, not the
  microphone, so the stream has no audio by default (`stream_audio: false`; see FAQ). To talk to Vector,
  hold 🎙 on the control card: the recording plays on his speaker.
- Card thumbnails show the last cached frame and never wake the camera. "Refresh camera image" grabs a
  fresh one.
- Photos Vector takes ("Hey Vector, take a photo" or the button) appear as the "Last photo" image entity
  and in **Media → Vector**.
- If the robot's camera hangs (it happens after a nap on the charger), the bridge gives control back after
  two attempts and the card shows "Camera is not responding" — restarting the robot helps.

## Voice → Home Assistant
"Hey Vector, turn on the living room light" is handled by Home Assistant (Assist) and Vector says the
answer. What Assist does not understand is handled by wire-pod as before (clock, dance, weather, its LLM).

### How a sentence gets from wire-pod to Home Assistant
Speech is recognised by wire-pod, so wire-pod has to pass the sentence on. The app creates a **wire-pod
system intent `ha_router`** through wire-pod's API (`POST /api/add_custom_intent`):

```json
{
  "name": "ha_router",
  "utterances": ["*"],
  "intent": "intent_system_noaudio",
  "exec": "/usr/bin/curl",
  "execargs": ["-s", "-m", "10", "-A", "!botSerial", "-H", "Content-Type: text/plain",
               "--data-binary", "!speechText",
               "http://<HA host IP>:8790/wirepod/speech?token=<token>"],
  "issystem": true
}
```

- `utterances: ["*"]` matches every sentence, and wire-pod checks custom intents **before** its built-in
  ones — so every sentence after "Hey Vector" comes here first.
- `exec` / `execargs`: wire-pod runs `curl` on its server, which sends the sentence to the app.
  `!speechText` becomes the recognised text, `!botSerial` the robot's serial number (sent as the
  User-Agent). curl waits at most 10 s.
- `issystem: true`: wire-pod reads curl's answer:
  - `{"status":"ok","returnIntent":"intent_system_noaudio"}` (Home Assistant handled it) → the robot gets
    "do nothing", Vector stays silent and the integration makes him say Assist's answer,
  - anything else, an error or no answer → the intent "does not match" and wire-pod handles the sentence
    its own way.

```
Vector → wire-pod (speech recognition) → curl → app :8790 → integration → Assist
                                       ← "handled" / "pass" ←
```

### Why port 8790
The app connects to the robot by itself (the robot's port 443) — nothing has to be exposed for that.
The sentence flows the other way though: from the wire-pod server to Home Assistant, so the app needs a
door reachable from the network: port **8790** on the Home Assistant host (app network settings,
`8790/tcp`; change it if something else uses it). It only accepts `POST /wirepod/speech` and
`POST /v1/chat/completions` and requires a token (`/data/hook_token`, random on the first start).
The host address comes from the Supervisor (IP of the primary interface); the `hook_host` option
overrides it. After changing the port the app fixes the address in wire-pod by itself.

### Who manages it
- On start and every 5 minutes the app makes sure wire-pod holds exactly one up-to-date `ha_router`
  (address, port, token); a missing or outdated one is removed and added again. Manual edits get
  overwritten (and editing it in wire-pod's panel would clear `issystem` anyway).
- The **"Voice → Home Assistant"** switch in the integration removes / restores the intent; its state is
  remembered (`/data/voice_router`), the `voice_router` option is only the initial state.
- When Home Assistant, the app or the integration is down, curl fails at once and wire-pod works as usual
  — Vector never goes "deaf".

### The intent is not shown in wire-pod's panel
wire-pod's **Custom Intents** page deliberately hides system intents (`issystem: true`), so the list looks
empty although `ha_router` is active. To see it (including the token):
`http://<wire-pod>:8080/api/get_custom_intents_json`.

To remove it: switch "Voice → Home Assistant" off. If the app is gone (e.g. uninstalled — the intent stays
in wire-pod and every sentence first tries a dead address):

```
curl -X POST -H "Content-Type: application/json" -d '{"number":1}' \
     http://<wire-pod>:8080/api/remove_custom_intent
```

`number` is the intent's 1-based position in `get_custom_intents_json`.

### What stays with Vector
- Assist understands some of Vector's own commands too (e.g. "what time is it"). So that Vector keeps
  showing his clock, the integration has a **"Keep on Vector"** list (integration options): sentences with
  these phrases go straight to wire-pod. The default list depends on the Home Assistant language.
- Also in the options: the Assist agent (default: the built-in one, no LLM) and the answer voice
  (wire-pod's TTS or any Home Assistant TTS engine). With an LLM agent every sentence is "understood" and
  handled by Home Assistant, and Vector loses his own commands (except the "Keep on Vector" list).
- Every sentence ends up in the "Last speech" sensor, the "Speech" event entity and the `vector_speech`
  event (text, handled or not, answer).

### Custom voice commands
In the integration (**Settings → Vector → Add command**) you define sentences (Assist syntax), an answer,
a script and a reaction of the robot. They are checked in the integration before the "Keep on Vector"
list and before Assist — nothing to change in wire-pod (`ha_router` passes every sentence anyway).

### wire-pod LLM → Home Assistant
The "wire-pod LLM → Home Assistant" switch points wire-pod's LLM (what wire-pod does with sentences nobody
handled — questions like "what is the capital of France") at the app: `http://<HA host IP>:8790/v1`
(OpenAI format, the token as the key). The agent chosen in the integration options answers. wire-pod's own
LLM settings (provider, address, key, model) are saved in `/data` and restored when the switch is turned
off; the prompt and everything else stay untouched.

### Troubleshooting
The app log shows every decision, e.g.
`voice router text="turn on the living room light" handled=true took=552ms`;
wire-pod's log shows `Intent matched: intent_system_noaudio` for handled sentences.

## Dashboard: control card
The integration ships a Lovelace card (no resource to add) — live camera and hold-to-drive controls:

```yaml
type: custom:vector-control-card
camera: camera.vector
# speed: 120      # mm/s forward (optional)
# hold: true      # take control while the card is open (default)
```

- Arrows (forward, back, turn), head and lift work while you hold them: the card repeats `vector.move`
  every 0.3 s and Vector stops 0.7 s after the last call (also when the network or the page goes away).
- Behavior control is taken while you steer: the full-screen view holds it while open, the tile
  from the first drive until a minute after the last one; it goes back to Vector when the view
  closes or the page goes to the background. The "Hold" button keeps it until pressed again.
- On the charger a bar offers "Drive off charger"; the forward arrow does the same.
- ▶ starts the live view (WebRTC with sound when the live stream is on, MJPEG otherwise); ✕, 5 minutes
  without a touch or the page going to the background stop it. Closing the card stops the camera.
- ⛶ opens the full-screen control view: landscape, the camera fills the screen, the driving pad on the
  left, listening (🔊 — Vector's microphone), head and lift on the right, speed and charger at the bottom.

The same card in a popup ([browser_mod](https://github.com/thomasloven/hass-browser_mod)) opened from a
tile, with a few behaviors below the camera:

```yaml
type: tile
entity: sensor.vector_battery
name: Vector
icon: mdi:robot-happy
tap_action:
  action: fire-dom-event
  browser_mod:
    service: browser_mod.popup
    data:
      title: Vector
      content:
        type: vertical-stack
        cards:
          - type: custom:vector-control-card
            camera: camera.vector
          - type: grid
            columns: 4
            square: false
            cards:
              - type: tile
                entity: button.vector_dance
                vertical: true
                hide_state: true
                tap_action:
                  action: perform-action
                  perform_action: button.press
                  target:
                    entity_id: button.vector_dance
              - type: tile
                entity: button.vector_look_at_me
                vertical: true
                hide_state: true
                tap_action:
                  action: perform-action
                  perform_action: button.press
                  target:
                    entity_id: button.vector_look_at_me
              - type: tile
                entity: button.vector_fist_bump
                vertical: true
                hide_state: true
                tap_action:
                  action: perform-action
                  perform_action: button.press
                  target:
                    entity_id: button.vector_fist_bump
              - type: tile
                entity: button.vector_go_to_sleep
                vertical: true
                hide_state: true
                tap_action:
                  action: perform-action
                  perform_action: button.press
                  target:
                    entity_id: button.vector_go_to_sleep
```

Entity ids depend on the Home Assistant language at the time the integration was added (e.g. a Polish
installation has `sensor.vector_bateria`, `button.vector_tancz`) — check them under Settings → Devices.

## Robots Code Studio (children's programs)
Robots Code Studio is a block / Python editor for children. From app
0.3.0 it can run a child's program on Vector step by step: the studio executes the program and sends
the app one command per block, inside a **program session**. While a program runs:

- the app keeps Vector's behavior control for the whole program (Vector does not drive off to his
  charger in the middle of a "wait 3 s" block);
- the Home Assistant card and automations cannot move Vector, animate him or speak through him —
  they get the error `program running`, and the card shows "Child's program running". **Stop always
  works**: Stop on the card (or the `stop` command, a script, an automation) ends the program at once;
- if the studio stops answering for a few seconds (laptop closed, Wi-Fi gone) the app stops the motors
  and gives Vector his freedom back (watchdog);
- a session lasts at most `limit` seconds (default 5 minutes, at most 30);
- if Vector takes control back himself — he saw a **table edge (cliff)**, he was **picked up** — the
  program ends and the studio tells the child why. This is on purpose: Vector's own reflexes protect him.

### Turning it on
1. In the app's **Configuration**, set **Studio token** (`studio_token`) to a password of your choice and
   save. Empty = the studio cannot connect.
2. In the app's **Network** section, enter a port next to `3012/tcp` (e.g. `3012`) and save; restart the
   app. By default port 3012 is reachable only inside Home Assistant (the integration uses it there).
3. In the studio, enter `ws://<Home Assistant address>:3012/ws` and the studio token.

The studio token only opens what a program needs: motion, speech, animations, eye colour, the cube,
the sensors and the state. It cannot change settings, rename or delete faces, read or delete photos,
open the camera or the microphone, or switch the voice router. Motion commands with the studio token
also need the studio's own program session. The integration keeps its own token (`/data/ws_token`).

### Units
The app speaks Vector's units: millimetres, mm/s, degrees, and **a positive angle turns left**
(Vector's convention). The studio shows children centimetres and "positive = right" and converts.

## FAQ
**The camera looks like a slideshow — a new snapshot every second.**
The app is older than 0.2.0 (or `live_stream` is off), so the card can only reload JPEG frames. Update the
app and the integration to 0.2.0 or newer and tap ▶ on the card: the camera entity then has a real stream
(WebRTC). Vector's camera itself still gives about 2 new pictures per second.

**The card shows a broken-image icon over "Vector is unavailable", and the status says "unavailable %".**
The robot is not connected (the app log shows `robot disconnected … no route to host`: Vector is off, flat
or not on Wi-Fi). Integration 0.2.0 shows a proper "Vector is unavailable" screen and "—" instead; turn
Vector on or put him on the charger and the card comes back by itself.

**The full-screen view stays black while the tile shows the picture.**
Home Assistant 2026's camera player takes its settings from inside the Home Assistant page; the
full-screen view used to be placed outside it, so the player drew nothing. Fixed in integration
0.2.2. Update it and reload the page.

**The live view stops or never starts; the HA log shows `Error from stream worker … Invalid data
found` or go2rtc `i/o timeout`.**
Home Assistant's go2rtc waits only 5 s for the stream, and Vector's camera needs longer on the
charger. Since app 0.2.2 + integration 0.2.2 the integration first asks the app to start the
stream (log: `live stream warm`) and only then opens it.

**The live view only beeps.**
Vector's firmware never connected the SDK audio feed to the microphones: it sends a 1 kHz test tone
(`sdkComponent.cpp`: "temporary 1 kHz sine wave generator to test over the wire transfer"). So you
cannot listen to Vector through the SDK; `stream_audio` is off by default. Talking works the other
way: hold 🎙 on the card and Vector plays your voice (integration 0.2.3+).

**A child's program stopped by itself ("Vector saw an edge" / "Vector was picked up").**
Vector's own safety reflexes took control back: a cliff sensor saw a table edge, or somebody lifted
him (event `program_ended` with `reason: control_lost`, `cause: cliff | picked_up | falling | wake_word
| robot`). This cannot be switched off (that would also switch off the edge reflex). Put Vector back on
the floor or away from the edge and start the program again. Other reasons: `watchdog` — the studio
stopped answering; `limit` — the time limit; `ha_stop` — Stop in Home Assistant; `disconnected` —
the robot or the studio connection went away.

**The studio cannot connect to Vector.**
Check, in this order: port `3012/tcp` has a number in the app's **Network** section (empty = only
inside Home Assistant) and the app was restarted; the studio uses `ws://<Home Assistant IP>:<that
port>/ws`; the **Studio token** option is set and the studio uses exactly that value (HTTP 401 =
wrong or empty token). The app log says `studio connected` when it works. A studio command answered
`not allowed with the studio token` is one the studio may not send (settings, faces, photos, camera).

**The Home Assistant card does nothing while a child's program runs.**
That is the program lock: during a program only the program moves Vector. The card shows "Child's
program running" with its controls greyed out; tap **Stop** to end the program and get the card back.
Home Assistant announcements through Vector's speaker wait too (`POST /audio` answers 409).

**The live view has no sound.**
Browsers only play sound after a tap: open the full-screen view (⛶) and tap 🔊. If 🔊 says the sound needs
a newer app, update the app to 0.2.0 or newer and check that `stream_audio` is on. If the sound plays too
fast or too slow, set `mic_sample_rate` to the value the app log shows as `sample_rate_hz`.

## Limitations
- No images on the face screen: on Vector 2.0 (firmware 2.0.1.6076ep) the SDK call
  `DisplayFaceImageRGB` crashes the robot (error 914/915, then a restart) — the robot's SDK server
  assumes the Vector 1.0 screen.
- The camera gives ~2 new pictures per second (the stream is smooth, but motion updates that often).
- No backpack lights: the SDK has no call for them. The microphone cannot be read (test tone).
- `drive_on_charger` does not search for the charger: it works only when Vector sees it.

## Configuration
| Option | Description |
|---|---|
| `wirepod_url` | wire-pod address, e.g. `http://192.168.1.20:8080` (`.local` names may not resolve inside the app). |
| `esn` | Robot serial number; empty = the first robot in wire-pod. |
| `name` | Robot name in Home Assistant. |
| `robot_ip`, `guid` | Override what wire-pod reports (normally empty). |
| `voice_router` | Initial state of the voice router (later the switch in Home Assistant decides). |
| `hook_host` | Address wire-pod uses to reach the app; empty = the Home Assistant host IP. |
| `log_level` | `debug` logs every robot event. |
| `live_stream` | Real video (go2rtc → WebRTC in Home Assistant) instead of MJPEG only. Default on. |
| `stream_audio` | Adds the SDK audio feed to the stream. Default off: Vector's firmware sends a 1 kHz test tone there, not the microphone (see FAQ). |
| `mic_sample_rate` | Microphone sample rate (default 12000, measured on a Vector 2.0); use the value from the log if the sound plays too fast or too slow. |
| `studio_token` | Password for Robots Code Studio (children's programs); empty = no studio. Publish port 3012 too (see Robots Code Studio). |

After starting, the app announces itself to the integration (Supervisor discovery) — a discovered Vector
integration appears under **Settings → Devices & services**. The API token is in `/data/ws_token`.

## API (for the integration)
`GET /ws` (header `Authorization: Bearer <token>`):
- `{"type":"hello","version","api":1,"features":["program"],"program":null,"robots":[state],"voice_router":{…},"stream":{"rtsp_port":8554,"path":"vector","username":"vector","audio":true}}`
  after connecting (`stream` only when the live stream is on; `program` = `{owner, since, limit}` while
  a program runs; `"role":"studio"` for a studio connection),
- `{"type":"event","event":"state","robot":"<esn>","state":{…}}` — the full state,
- `{"type":"event","event":"<name>","robot":"<esn>","data":{…}}` — `wake_word` (`phase` begin/end),
  `touched`, `picked_up`, `cliff`, `button_pressed`, `face_seen` (`name`, `face_id`), `intent`,
  `cube_tapped`, `cube_connected`, `cube_disconnected`, `control_lost`, `photo_taken`, `audio_started`,
  `audio_finished`, `speech` (`id`, `text`), `llm` (`id`, `text`), `voice_router`, `program_started`
  (`owner`, `since`, `limit`, `heartbeat`), `program_ended` (`owner`, `reason`, `cause`, `duration`),
- a command `{"id":1,"command":"…","robot":"<esn>","args":{…}}` → `{"type":"result","id":1,"success":…}`.

Commands: `say_text {text, engine}` (`engine`: `wirepod` — default, wire-pod's TTS without behavior
control; `vector` — Vector's built-in English voice), `play_animation {name, loops}`,
`play_animation_trigger {name, loops}`, `list_animations`, `list_animation_triggers`,
`app_intent {intent, param}`, `drive_on_charger`, `drive_off_charger`, `drive_straight {distance, speed}`
(mm, mm/s), `turn_in_place {angle, speed}` (°, °/s), `drive_wheels {left, right, duration}`,
`set_head_angle {angle}` (−22…45°), `set_lift_height {height}` (0…100 %), `stop`,
`set_eye_color {hue, saturation}` (0…1), `update_settings {master_volume, eye_color, locale, time_zone,
default_location, dist_is_metric, temp_is_fahrenheit, clock_24_hour, button_wakeword}`,
`hold_control {seconds}`, `release_control`, `list_faces`, `rename_face {face_id, name}`,
`delete_face {face_id}`, `enroll_face {name}`, `connect_cube`, `disconnect_cube`, `flash_cube`,
`forget_cube`, `pickup_cube`, `roll_cube`, `dock_with_cube`, `go_to_cube {distance}`,
`go_to {x, y, angle, relative}`, `turn_towards_face {face_id}`, `set_clock_24h {on}`,
`set_voice_router {enabled}`, `set_llm_proxy {enabled}`, `speech_decision {id, handled}`,
`llm_reply {id, text}`, `refresh`, `move {left, right, head, lift}` (mm/s, rad/s; Vector stops 0.7 s after
the last one), `stop_audio`, `set_audio_volume {volume}`, `list_photos`, `delete_photo {id}`,
`camera_snapshot`.

HTTP (same token): `GET /snapshot[/<esn>]?cached=true` (last frame, 404 when none) or `?max_age=<s>`
(fresh, starts the camera), `GET /mjpeg[/<esn>]` (multipart JPEG), `GET /mic[/<esn>]?rate=16000` (the
robot's microphone as raw 16-bit mono PCM, silence filled in when the robot sends nothing),
`GET /photo/<esn>/<id>[?thumb=true]`,
`POST /audio[/<esn>]?volume=&interrupt=true` (audio file in the body, answer 202).

### Program sessions (API, app 0.3.0+)
Check `hello.features` contains `"program"` (older apps do not have it). All commands below go over the
same WebSocket; `session` is the id `program_begin` returned — it is never broadcast.

| Command | Arguments | Result |
|---|---|---|
| `program_begin` | `owner` (text, e.g. `"rcs"`), `limit` (s, 10–1800, default 300), `heartbeat` (s, 1–30, default 3), `drive_off_charger` (bool, default false) | `{session, limit, heartbeat, state, sensors}` |
| `program_heartbeat` | `session` | `{remaining, sensors}` (`remaining` = seconds to the limit) |
| `program_end` | `session`, `reason` (optional: `end`, `stop`, `error`…, default `end`) | `null` |
| `sensors` | (`session` optional) | the sensors, at once (see below) |
| `set_wheels` | `session`, `left`, `right` (mm/s, ±220) | `null` — keeps driving until `set_wheels 0 0`, `stop`, the program's end or the watchdog |
| `set_cube_lights` | `color` (`"#rrggbb"`) or `hue` + `saturation` (0–1), `mode` (`on` default, `flash`, `off`) | `null` (the cube must be connected) |

Errors of `program_begin`: `program running` (another session runs), `robot not connected`,
`battery low` (battery level low or below 10 %), `behavior control not granted`,
`drive off charger: …`. Other commands: `no such program session` (it ended — read `program_ended`),
`program session required` (the studio sent a motion command without its session).

- **Heartbeat:** send `program_heartbeat` at least every `heartbeat / 2` seconds — also while waiting for a
  long command such as `drive_straight`. Every command with `session` renews the watchdog too. No
  heartbeat for `heartbeat` seconds → motors stop, the running action is cancelled, the app's audio stops,
  control is released and `program_ended {reason: "watchdog"}` is sent.
- **Inside a session** use the normal commands with `"session": "<id>"` in `args` (`drive_straight`,
  `turn_in_place`, `say_text`, `play_animation_trigger`, `set_head_angle`, …). Sounds:
  `POST /audio?session=<id>`.
- **Lock:** during a session, motion / animation / speech / control commands from anybody else (without
  the session id and not from the connection that began it) fail with `program running`; reading
  (`refresh`, `sensors`, `list_*`), state and events still reach everybody. `stop` always works and ends
  the program (`reason: "ha_stop"`; `"stop"` when the program itself sends it).
- **End reasons** (`program_ended.reason`): the program's own reason (`end`, `stop`, `error`…),
  `watchdog`, `limit`, `ha_stop`, `control_lost` (+ `cause`: `cliff`, `picked_up`, `falling`,
  `wake_word`, `robot`, `connection`), `disconnected` (+ `cause`: `client` — the program's WebSocket
  closed, `robot` — Vector went away).

`sensors` (Vector's units: mm, degrees, mm/s; angles + = left):
```json
{"at": 1795428977.318, "connected": true,
 "touched": false, "picked_up": false, "held": false, "falling": false, "cliff": false,
 "moving": false, "listening": false,
 "obstacle_mm": 123, "obstacle_ok": true, "obstacle_clear": false, "obstacle_signal": 0.8, "lift_in_fov": false,
 "on_charger": false, "charging": false, "battery": {"level": "nominal", "volts": 3.9, "percent": 60, "charging": false, "on_charger": false},
 "head_angle": 10, "lift_height": 0, "pose": {"x": 120, "y": -30, "angle": 45},
 "face": {"id": 7, "name": "Alice", "age_s": 0.4},
 "cube": {"connected": true, "visible": true, "seen_age_s": 0.2, "taps": 1},
 "last_speech": {"text": "dance for me", "age_s": 3.1},
 "accel": {"x": 1, "y": 2, "z": 9800}, "gyro": {"x": 0, "y": 0, "z": 0}, "wheels": {"left": 0, "right": 0},
 "counts": {"touches": 1, "button_presses": 1, "picked_up": 0, "cliff": 0, "wake_words": 1, "speech": 1, "cube_taps": 1}}
```
`obstacle_mm` is a real distance only when `obstacle_ok` is true (an object in the sensor's range and the
lift not in front of it); `face` is the last face seen (`age_s` ≤ 2 = sees it now; `name` empty = an unknown
face; `null` = none yet); `last_speech` is what wire-pod heard after "Hey Vector" (`null` = nothing yet);
`counts` count from `program_begin` (or from the app's start when no program runs).

Example:
```json
→ {"id":1,"command":"program_begin","args":{"owner":"rcs","limit":300,"heartbeat":3}}
← {"type":"result","id":1,"success":true,"result":{"session":"5f0c…","limit":300,"heartbeat":3,"state":{…},"sensors":{…}}}
← {"type":"event","event":"program_started","robot":"00a1b2c3","data":{"owner":"rcs","since":1795428977,"limit":300,"heartbeat":3}}
→ {"id":2,"command":"drive_straight","args":{"session":"5f0c…","distance":100,"speed":100}}
→ {"id":3,"command":"program_heartbeat","args":{"session":"5f0c…"}}
→ {"id":4,"command":"program_end","args":{"session":"5f0c…","reason":"end"}}
← {"type":"event","event":"program_ended","robot":"00a1b2c3","data":{"owner":"rcs","reason":"end","duration":12.3}}
```
