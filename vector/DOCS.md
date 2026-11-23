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
  (re)connection — nothing to copy by hand. If you set **both** `robot_ip` and `guid` (and `esn`), the app
  never asks wire-pod for them (see "Connecting without asking wire-pod").
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
the sensors, the state and (from 0.4.0) a read-only camera view. It cannot change settings, rename or
delete faces, read or delete stored photos, open the microphone, or switch the voice router. Motion
commands with the studio token also need the studio's own program session. The integration keeps its
own token (`/data/ws_token`).

**Camera view for the studio (app 0.4.0+).** The studio token (and the parent token) may also *watch*
the camera: `GET /mjpeg[/<esn>]` and `GET /snapshot[/<esn>]` with `Authorization: Bearer <studio token>`
(or `?token=`), so the studio can show a child what Vector sees. Read only: no `/mic`, no stored
photos (`/photo`, `list_photos`, `delete_photo`), no camera settings — those answer **403**. The
studio should fetch the view on its server and pass the pictures on (the token never goes to the
browser). No program session is needed. Watching works like on the card: **off the charger** the
picture comes at once without behavior control; **on the charger** Vector's camera sends nothing
without control, so the app takes control while somebody watches (Vector holds still) — during a
program it already has it. While a `run_behavior` runs, the app does **not** take control for the
camera (that would stop the behavior): on the charger the view stays dark until the behavior ends.

### Voices, listening, Vector's behaviors, AI and weather (app 0.4.0+)
Programs can also speak with a chosen voice and a funny effect, wait for what a child says, let Vector
do one of his own tricks (dance, come here, find the cube…), ask a local AI a question, tell the
weather and change the volume. Set it up once:

1. **Voices** — option `piper_url`: the Wyoming Piper server Home Assistant already uses, e.g.
   `tcp://192.168.1.20:10200`, and optionally `piper_voice` (e.g. `pl_PL-gosia-medium`). The app talks to
   Piper itself (Wyoming protocol), turns the sentence into a WAV and plays it on Vector's speaker. Empty
   = programs speak through wire-pod's `say_text` with wire-pod's voice (no voice choice, no effects).
2. **Listening** — needs the voice router (port `8790/tcp` published, wire-pod reachable): Vector sends
   what he hears after "Hey Vector" only to wire-pod, and wire-pod hands every sentence to this app.
   While a program that began with `claim_speech: true` runs, those sentences go **to the program
   only**: Home Assistant does not see them and Vector stays silent. This works even when the
   "Voice → Home Assistant" switch is off (the app keeps its wire-pod intent for the program).
3. **AI for children** — options `ai_url` (a local [Ollama](https://ollama.com) server, e.g.
   `http://192.168.1.30:11434`) and `ai_model`. Empty `ai_url` = off. Every question is a new chat
   (no history), with a fixed system prompt for children aged 7–12: Polish, at most `max_words`
   words, no links, e-mails or phone numbers, no questions about the child, and "ask a parent" for
   unsuitable topics; links, markdown and emoji are also removed from the answer. Questions and answers
   are not logged or stored by the app. The AI only returns text — it never controls the robot; the
   program decides whether to say it (`say_voice`).
   **Which model:** a 4–8B *instruct* (non-thinking) model that speaks Polish well, e.g.
   `qwen3-vl:8b-instruct`, `gemma3:4b` or a Bielik 4.5B/7B build. Avoid 20B+ models (slow, and they share
   the GPU with speech recognition) and "thinking" models (long pauses). The first question after a
   while loads the model (up to ~20 s); later answers take 1–5 s.
4. **Weather** — the app has read-only access to Home Assistant (`homeassistant_api`); option
   `weather_entity` (e.g. `weather.home`), empty = the first `weather.*` entity.
5. **Photos and teaching faces** — only for a parent's account in the studio: option
   `studio_parent_token`, a **second, different** password. The studio uses it only when a parent runs
   the program; the child's `studio_token` can never take photos or enroll faces. Empty = never from
   the studio. A photo goes to the studio as an image in the command's answer and is stored nowhere.

#### What needs wire-pod, and what does not
| Feature | Needs wire-pod? |
|---|---|
| Connecting to Vector | Only to look up the IP and GUID — not when `robot_ip` + `guid` are set |
| `say_voice` / `list_voices` with `piper_url` | No (straight to Piper) |
| `say_text` (engine `wirepod`), `say_voice` without Piper or when Piper fails | Yes (`/api-sdk/say_text`) |
| `listen`, `claim_speech`, "when I hear…" | **Yes** — Vector sends his microphone only to wire-pod (speech recognition) |
| `listen {wake: true}` | Yes (`/api-sdk/trigger_wake_word`; may not work on production firmware) |
| `ask_ai` | No (straight to Ollama; wire-pod's own LLM is not used) |
| `weather` | No (Home Assistant) |
| `run_behavior`, `set_volume`, `mirror_mode`, `take_photo`, `sensors` | No (the robot's SDK) |

#### Connecting without asking wire-pod
Set `esn`, `robot_ip` and `guid` once, then the app talks to Vector directly. Where to find them:
wire-pod's page `http://<wire-pod>:8080/api-sdk/get_sdk_info` lists each robot's `esn`, `ip_address`
and `guid` (empty `guid` = use `global_guid`); the integration's diagnostics show the robot's ESN and
IP too. Give Vector a fixed IP address in your router — with a hand-set IP the app does not follow a
new address. wire-pod is still needed for listening and wire-pod's voice.

### Stand still (app 0.5.0+)
One switch keeps Vector still between programs — he does not roam, turn or fidget, so a parent can
test and children can program without carrying him back each time. In Home Assistant: the switch
**Stand still** (or the icon next to the robot's name on the control card); from the studio: the
**Stand still** toggle or `program_begin {stay_still: true}`.

- On: the app holds Vector's behavior control (the same as during a program). He stands, eyes as
  usual; Home Assistant can still drive him, speak, animate.
- A program takes over as usual; when it ends the app keeps the **same** control — there is no moment
  in which Vector could drive away (`go_to_charger` is the exception: it lets him go home).
- `stop` only stops, and **release control** (the card after driving, the switch Manual control) only
  ends the manual hold: Vector keeps standing still.
- Vector is let go by switching it off, a low battery (< 10 % or "low"), 60 min
  without a program (option `stay_still_timeout_min`), `go_to_charger` and the app stopping. The event
  `stay_still_ended {reason}` says why (`battery_low`, `timeout`, `charger`).
- Picked up or a cliff: Vector takes control back; the app asks for it again every 5 s.
- On the charger it is allowed: Vector charges by the contacts while the app holds control.

### Units
The app speaks Vector's units: millimetres, mm/s, degrees, and **a positive angle turns left**
(Vector's convention). The studio shows children centimetres and "positive = right" and converts.

## FAQ
**Vector stands and does nothing, or wakes up only after an hour.**
Stand still is on (the switch **Stand still**, the state `stay_still: true`): the app holds his control
between programs. Switch it off — or wait: it lets go by itself after `stay_still_timeout_min` without a
program, on a low battery and when he goes to the charger.

**Stand still is on, but Vector moved for a moment.**
He was picked up or met a cliff: then Vector takes control back for safety, and the app asks for it
again within ~5 s.

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

**The studio's camera view is black while Vector is on the charger.**
On the charger Vector's camera sends pictures only under behavior control. The app takes control for
the camera by itself (Vector then holds still), except while a program runs one of Vector's own
behaviors (`run_behavior`) — then the view stays black until the behavior ends. If it stays black
otherwise: the robot's camera hung after a nap on the charger (log: `camera: no frames even with
behavior control`) — restart Vector. HTTP 403 on `/mic` or `/photo` with the studio token is
expected (only the camera view is open to the studio).

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
`not allowed with the studio token` is one the studio may not send (settings, faces, stored photos).

**The Home Assistant card does nothing while a child's program runs.**
That is the program lock: during a program only the program moves Vector. The card shows "Child's
program running" with its controls greyed out; tap **Stop** to end the program and get the card back.
Home Assistant announcements through Vector's speaker wait too (`POST /audio` answers 409).

**"Ask AI" answers `ai is off (set the add-on option ai_url)`.**
Set `ai_url` (e.g. `http://192.168.1.30:11434`) and `ai_model` and restart the app. The log shows
`ask_ai: local LLM` when it is on (`ask_ai off` + the reason when an option is wrong).

**"Ask AI" says `ai: no answer within 45s` or `ai: HTTP 404 model '…' not found`.**
The first question loads the model; a big model on a busy machine can take longer than 45 s — use a
smaller model (4–8B). `not found`: the model name in `ai_model` is not on the Ollama server
(`ollama list` shows the names). `connection refused`: Ollama is not reachable from Home Assistant
(Ollama listens only on localhost by default: set `OLLAMA_HOST=0.0.0.0`).

**The weather block says `weather needs Home Assistant` or `no weather entity`.**
The app needs `homeassistant_api` (on since 0.4.0 — update and restart the app) and a `weather.*`
entity. Set `weather_entity` if you have several; `… is unavailable` means the weather integration
itself has no data right now.

**The forecast is empty and the answer has `forecast_unavailable: true`** (0.4.0: `forecast: home
assistant /api/services/weather/get_forecasts?return_response: HTTP 400`).
The chosen weather entity has no daily forecast (Developer tools → States: `supported_features` without
bit 1). Since 0.4.1 the app then returns the weather now without an error. For forecasts set
`weather_entity` to an entity that has them (e.g. Met.no).

**"Hey Vector" during a child's program does not turn on the light.**
That is on purpose: a program that listens (`claim_speech`) takes every sentence after "Hey Vector",
so a child's words never switch things in the house and Vector does not answer them himself. Home
Assistant gets sentences again as soon as the program ends (or Stop is pressed).

**A program's voice sounds like wire-pod's instead of the chosen one.**
`say_voice` falls back to wire-pod when Piper cannot be reached (answer `fallback: true` with the
error). Check `piper_url` (`tcp://<host>:10200`) and that the voice name is in `list_voices`.

**`listen` never hears anything.**
Listening needs the voice router: port `8790/tcp` published and the app log line `Voice → HA: hook …`.
The child must say "Hey Vector" first (or the program uses `wake: true`, which does not work on every
firmware). `program_begin` answers `claim_speech: false` when the voice router is not available.

**`run_behavior` answers `reason: not_started`.**
Vector did not take the behavior (he was busy, on the charger for some behaviors, or his firmware does
not accept that intent from the SDK). The program goes on; try again off the charger.

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
| `studio_parent_token` | A second, different password the studio uses for a parent's account: also `take_photo` and `enroll_face`. Empty = never from the studio. |
| `piper_url`, `piper_voice` | Wyoming Piper server (`tcp://host:10200`) and default voice for `say_voice`; empty = wire-pod speaks. |
| `ai_url`, `ai_model` | Local Ollama server and model for `ask_ai`; empty = off. |
| `weather_entity` | Weather entity for `weather`; empty = the first `weather.*`. |
| `program_stay_still` | After a studio program Vector stands still (see Stand still). Default on; the studio may say otherwise per program. |
| `stay_still_timeout_min` | Stand still lets Vector go after this many minutes without a program (1–480, default 60). |

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

HTTP (same token; `/snapshot` and `/mjpeg` also with the studio tokens, the rest answers them 403): `GET /snapshot[/<esn>]?cached=true` (last frame, 404 when none) or `?max_age=<s>`
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
From app 0.4.0 `sensors` also has `"stimulation": 0.4` (how excited Vector is, 0–1), `"volume": 3`
(master volume 0 mute … 5 high; `null` = not read yet) and `"life": {"alive_days": 1460.2,
"pet_minutes": 312.5, "distance_m": 826.8, "wake_words": 2412}` (lifetime statistics, refreshed every
10 minutes; a field is `null` when the robot does not report it).

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

### Stage V5 commands (app 0.4.0+): contract for the studio
`hello.features` lists what this app can do: always `program`, `behavior`, `volume`, `mirror`; `listen`
with the voice router; `voices` with `piper_url`; `ai` with `ai_url`; `weather` with Home Assistant
access; `parent` when `studio_parent_token` is set. `hello.parent` is `true` for a parent-token
connection. "Session" = the command needs the caller's own program session (`session` in `args`).

| Command | Arguments | Result | Session | Studio token |
|---|---|---|---|---|
| `program_begin` (new argument) | + `claim_speech` (bool, default false) | + `claim_speech` (true only when the voice router is available) | — | yes |
| `list_voices` | — | `{voices: [{name, description, languages, speakers}], effects: ["none","mouse","giant","robot","echo","fast","slow"]}` | no | yes (free) |
| `say_voice` | `text` (≤300 characters), `voice` (from `list_voices`, default `piper_voice`), `effect` (default `none`), `wait` (bool, default true), `volume` (0–100, optional) | `{engine: "piper", duration}` (wait) / `{engine: "piper", queued}` / `{engine: "wirepod", fallback: true, error?}` | studio: yes | yes |
| `listen` | `timeout` (s, 2–30, default 8), `prompt` (≤200, spoken first), `voice`, `wake` (bool, default false — makes Vector listen without "Hey Vector"; experimental) | `{heard, text, waited, woke}` (`heard: false`, `text: ""` after the timeout) | yes, with `claim_speech` | yes |
| `run_behavior` | `behavior`: `dance`, `come_here`, `look_at_me`, `look_over_there`, `who_am_i`, `find_cube`, `fetch_cube`, `explore`, `go_to_charger`; `timeout` (s, 5–120; defaults 15–60 per behavior) | `{behavior, finished, reason: finished\|not_started\|timeout, seconds, control}`; `go_to_charger`: `{behavior, program_ended: true, reason: "charger"}` | yes | yes |
| `ask_ai` | `question` (≤200 characters), `max_words` (5–60, default 30) | `{answer, words, seconds, model}` | studio: yes | yes |
| `stay_still` | `on` (bool; missing = only read) | `{stay_still}`; errors `robot not connected`, `battery low` | no | yes (free) |
| `program_begin` (0.5.0) | + `stay_still` (bool; missing = option `program_stay_still`) | + `stay_still` | — | yes |
| `weather` | `entity` (optional), `forecast_days` (0–5, default 0) | `{entity, condition, condition_pl, temperature, unit, humidity, wind_speed, wind_unit, text, forecast: [{date, condition, condition_pl, temperature, templow, precipitation}], forecast_unavailable}` | no | yes (free) |
| `set_volume` | `level` (0 mute … 5 high) | `null`; the volume before the first change comes back at the program's end | yes | yes |
| `mirror_mode` | `enable` (bool) | `null`; switched off at the program's end | yes | yes |
| `take_photo` | — | `{image: "data:image/jpeg;base64,…", bytes, at}` | studio: yes | **parent token only** |
| `enroll_face` | `name` | `null` (Vector looks for a face and says the name) | studio: yes | **parent token only** |

Errors (the `error` text): `program session required` (no own session, or a session command from Home
Assistant), `listening needs program_begin with claim_speech`, `already listening`, `program ended`
(the session ended while listening), `a Vector behavior is running` (any motion/speech command of the
owner while `run_behavior` waits), `behavior: one of …`, `stopped` (`stop` / the program's end during
`say_voice`, `run_behavior`), `parent permission required` (child token), `not allowed with the studio
token` (command outside the list), `no own voices (set the add-on option piper_url)`, `text too long
(max 300 characters)`, `effect: one of …`, `voice: a voice name from list_voices`, `ai is off (set the
add-on option ai_url)`, `ai: no answer within 45s`, `ai: HTTP …`, `ai: empty answer`, `question
required`, `question too long (max 200 characters)`, `weather needs Home Assistant (homeassistant_api)`,
`no weather entity in Home Assistant`, `<entity> is unavailable`, `control not back after the behavior:
…` (the program then ends with `control_lost`, `cause: behavior`).

Times: `say_voice` with Piper ≈ synthesis 0.2–1 s + the sentence; the wire-pod fallback answers after
speaking too. `listen` returns at the first sentence or after `timeout` (+ the prompt). `ask_ai` ≤ 45 s
(first question loads the model). `weather` ≤ 15 s. `set_volume` ≈ 1 s (robot settings). `run_behavior`
≤ `timeout` + ~1 s to take control back; it ends with `not_started` after ~6 s if Vector did not start
the behavior.

Behavior of a session around `run_behavior`: the app lets go of behavior control (Vector needs it for
his own behavior), keeps the program lock (Home Assistant still gets `program running`), pauses the
watchdog, sends the intent, watches the robot's activity until the behavior ends (`finished`), the
time is up (`timeout`) or Vector did not start it (`not_started`), and takes control back. `stop` or
the program's end during a behavior stops the motors, but Vector may finish his animation.
`go_to_charger` ends the program first (`program_ended {reason: "charger"}`) — Vector needs his own
control to find and climb the charger — so it is always a program's last block.

Speech of a program: while a `claim_speech` session runs, every sentence goes to `sensors.last_speech`,
`counts.speech` and a waiting `listen`; the event `program_heard {chars}` carries only the length (no
text reaches other clients). Home Assistant gets speech back at the program's end.

