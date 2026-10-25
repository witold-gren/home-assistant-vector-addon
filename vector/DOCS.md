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

## Camera and photos
- The live view runs only while somebody watches (~2 frames per second — that is what the robot gives).
  Off the charger Vector keeps doing his things; **on the charger** frames only come with behavior
  control, so Vector holds still while you watch (and 10 s more).
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
- While the card is open it holds behavior control (Vector waits for your commands; renewed every
  5 minutes); closing it gives control back. The "Hold" button toggles this.
- On the charger a bar offers "Drive off charger"; the forward arrow does the same.
- Closing the card stops the camera.

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

## Limitations
- No images on the face screen: on Vector 2.0 (firmware 2.0.1.6076ep) the SDK call
  `DisplayFaceImageRGB` crashes the robot (error 914/915, then a restart) — the robot's SDK server
  assumes the Vector 1.0 screen.
- The camera gives ~2 frames per second.

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

After starting, the app announces itself to the integration (Supervisor discovery) — a discovered Vector
integration appears under **Settings → Devices & services**. The API token is in `/data/ws_token`.

## API (for the integration)
`GET /ws` (header `Authorization: Bearer <token>`):
- `{"type":"hello","version","api":1,"robots":[state],"voice_router":{…}}` after connecting,
- `{"type":"event","event":"state","robot":"<esn>","state":{…}}` — the full state,
- `{"type":"event","event":"<name>","robot":"<esn>","data":{…}}` — `wake_word` (`phase` begin/end),
  `touched`, `picked_up`, `cliff`, `button_pressed`, `face_seen` (`name`, `face_id`), `intent`,
  `cube_tapped`, `cube_connected`, `cube_disconnected`, `control_lost`, `photo_taken`, `audio_started`,
  `audio_finished`, `speech` (`id`, `text`), `llm` (`id`, `text`), `voice_router`,
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
(fresh, starts the camera), `GET /mjpeg[/<esn>]` (multipart JPEG), `GET /photo/<esn>/<id>[?thumb=true]`,
`POST /audio[/<esn>]?volume=&interrupt=true` (audio file in the body, answer 202).
