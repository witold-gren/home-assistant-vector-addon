🇬🇧 [English version](DOCS.md)

# Vector (Anki / Digital Dream Labs)

Mostek między robotem **Anki Vector** a integracją **Vector** dla Home Assistant
([home-assistant-vector-integration](https://github.com/witold-gren/home-assistant-vector-integration)).
Vector nadal działa z Twoim serwerem [wire-pod](https://github.com/kercre123/wire-pod) (rozpoznawanie
mowy, komendy głosowe); mostek komunikuje się z robotem bezpośrednio przez jego SDK (gRPC na porcie 443)
i udostępnia integracji API WebSocket.

## Wymagania
- Vector (1.0 lub 2.0) skonfigurowany z wire-podem i osiągalny z Home Assistant.
- Interfejs WWW wire-poda osiągalny z Home Assistant (domyślnie `http://escapepod.local:8080`; użyj
  adresu IP, jeśli nazwy `.local` nie są rozwiązywane wewnątrz aplikacji).
- Integracja Vector (HACS lub ręcznie) — zostanie wykryta automatycznie.

## Jak to działa
- Adres IP robota i klucz SDK (GUID) są odczytywane z wire-poda (`/api-sdk/get_sdk_info`) przy każdym
  (ponownym) połączeniu — nie trzeba niczego przepisywać ręcznie.
- Stan robota (bateria, ładowarka, podniesienie, dotyk, krawędź, sen, aktywność, twarze, słowo
  wybudzające, położenie, kostka) pochodzi ze strumienia zdarzeń robota; trafia do Home Assistant
  najwyżej raz na sekundę, a zdarzenia (dotyk, twarz, „Hey Vector”, zdjęcie, …) są wysyłane od razu.
- Ruch, animacje, własny głos Vectora i kamera na ładowarce wymagają **kontroli zachowań** (behavior
  control). Mostek przejmuje ją tylko na czas komendy i oddaje 2 s po ostatniej — wtedy Vector wraca do
  swoich zajęć. Ręczna kontrola (przełącznik w integracji lub karta sterowania) trwa najwyżej 10 minut.
- Gdy robot jest wyłączony lub rozładowany, encje stają się niedostępne, a mostek łączy się ponownie
  (od 1 s do 60 s).

## Głośnik
Integracja ma odtwarzacz multimediów „Głośnik”: `tts.speak` z dowolnym silnikiem (np. Piper), pliki
multimedialne i adresy URL audio są odtwarzane na głośniku Vectora (16 kHz mono) bez przerywania tego, co
robi. Nagrania trafiają do kolejki; komunikat przerywa bieżące odtwarzanie. Tekst można też wypowiedzieć
przez wire-pod (`vector.say_text`), więc z Wyoming TTS wire-poda (np. Piper) Vector mówi w dowolnym języku.

## Kamera i zdjęcia
- Podgląd na żywo działa tylko wtedy, gdy ktoś patrzy (~2 klatki na sekundę — tyle daje robot).
  Poza ładowarką Vector zajmuje się swoimi sprawami; **na ładowarce** klatki przychodzą tylko z kontrolą
  zachowań, więc Vector stoi nieruchomo, dopóki patrzysz (i jeszcze 10 s).
- Miniatury na kartach pokazują ostatnią zapamiętaną klatkę i nigdy nie budzą kamery. „Odśwież obraz
  kamery” pobiera świeżą klatkę.
- Zdjęcia zrobione przez Vectora („Hey Vector, zrób zdjęcie” lub przycisk) pojawiają się jako encja
  obrazu „Ostatnie zdjęcie” oraz w **Multimedia → Vector**.
- Jeśli kamera robota się zawiesi (zdarza się po drzemce na ładowarce), mostek oddaje kontrolę po dwóch
  próbach, a karta pokazuje „Kamera nie odpowiada” — pomaga ponowne uruchomienie robota.

## Głos → Home Assistant
„Hey Vector, włącz światło w salonie” obsługuje Home Assistant (Assist), a Vector wypowiada odpowiedź.
To, czego Assist nie rozumie, obsługuje wire-pod tak jak wcześniej (zegar, taniec, pogoda, jego LLM).

### Jak zdanie trafia z wire-poda do Home Assistant
Mowę rozpoznaje wire-pod, więc to wire-pod musi przekazać zdanie dalej. Aplikacja tworzy przez API
wire-poda (`POST /api/add_custom_intent`) **systemową intencję wire-poda `ha_router`**:

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

- `utterances: ["*"]` pasuje do każdego zdania, a wire-pod sprawdza własne intencje **przed**
  wbudowanymi — więc każde zdanie po „Hey Vector” trafia najpierw tutaj.
- `exec` / `execargs`: wire-pod uruchamia `curl` na swoim serwerze, a ten wysyła zdanie do aplikacji.
  `!speechText` zamienia się w rozpoznany tekst, `!botSerial` w numer seryjny robota (wysyłany jako
  User-Agent). curl czeka najwyżej 10 s.
- `issystem: true`: wire-pod czyta odpowiedź curla:
  - `{"status":"ok","returnIntent":"intent_system_noaudio"}` (Home Assistant obsłużył zdanie) → robot
    dostaje „nic nie rób”, Vector milczy, a integracja każe mu wypowiedzieć odpowiedź Assista,
  - cokolwiek innego, błąd lub brak odpowiedzi → intencja „nie pasuje” i wire-pod obsługuje zdanie
    po swojemu.

```
Vector → wire-pod (rozpoznawanie mowy) → curl → aplikacja :8790 → integracja → Assist
                                       ← „obsłużone” / „przekaż dalej” ←
```

### Dlaczego port 8790
Aplikacja sama łączy się z robotem (port 443 robota) — do tego nie trzeba niczego wystawiać.
Zdanie płynie jednak w drugą stronę: z serwera wire-poda do Home Assistant, więc aplikacja potrzebuje
drzwi osiągalnych z sieci: portu **8790** na hoście Home Assistant (ustawienia sieci aplikacji,
`8790/tcp`; zmień go, jeśli coś innego go używa). Przyjmuje on tylko `POST /wirepod/speech` i
`POST /v1/chat/completions` i wymaga tokenu (`/data/hook_token`, losowany przy pierwszym uruchomieniu).
Adres hosta pochodzi z Supervisora (IP głównego interfejsu); opcja `hook_host` go nadpisuje. Po zmianie
portu aplikacja sama poprawia adres w wire-podzie.

### Kto tym zarządza
- Przy starcie i co 5 minut aplikacja pilnuje, aby wire-pod miał dokładnie jedną aktualną intencję
  `ha_router` (adres, port, token); brakująca lub nieaktualna jest usuwana i dodawana ponownie. Ręczne
  zmiany zostaną nadpisane (a edycja w panelu wire-poda i tak wyczyściłaby `issystem`).
- Przełącznik **„Głos → Home Assistant”** w integracji usuwa / przywraca intencję; jego stan jest
  zapamiętywany (`/data/voice_router`), a opcja `voice_router` to tylko stan początkowy.
- Gdy Home Assistant, aplikacja lub integracja nie działa, curl od razu zwraca błąd i wire-pod działa
  jak zwykle — Vector nigdy nie „głuchnie”.

### Intencja nie jest widoczna w panelu wire-poda
Strona **Custom Intents** wire-poda celowo ukrywa intencje systemowe (`issystem: true`), więc lista
wygląda na pustą, choć `ha_router` jest aktywna. Aby ją zobaczyć (razem z tokenem):
`http://<wire-pod>:8080/api/get_custom_intents_json`.

Aby ją usunąć: wyłącz przełącznik „Głos → Home Assistant”. Jeśli aplikacji już nie ma (np. została
odinstalowana — intencja zostaje w wire-podzie i każde zdanie najpierw próbuje martwego adresu):

```
curl -X POST -H "Content-Type: application/json" -d '{"number":1}' \
     http://<wire-pod>:8080/api/remove_custom_intent
```

`number` to pozycja intencji (licząc od 1) w `get_custom_intents_json`.

### Co zostaje przy Vectorze
- Assist rozumie także niektóre własne komendy Vectora (np. „która godzina”). Aby Vector dalej
  pokazywał swój zegar, integracja ma listę **„Zostaw Vectorowi”** (opcje integracji): zdania z tymi
  frazami trafiają prosto do wire-poda. Domyślna lista zależy od języka Home Assistant.
- Również w opcjach: agent Assist (domyślnie wbudowany, bez LLM) i głos odpowiedzi (TTS wire-poda lub
  dowolny silnik TTS Home Assistant). Z agentem LLM każde zdanie jest „zrozumiane” i obsługiwane przez
  Home Assistant, a Vector traci własne komendy (poza listą „Zostaw Vectorowi”).
- Każde zdanie trafia do sensora „Ostatnia wypowiedź”, encji zdarzenia „Mowa” i zdarzenia
  `vector_speech` (tekst, czy obsłużone, odpowiedź).

### Własne komendy głosowe
W integracji (**Ustawienia → Vector → Dodaj komendę**) definiujesz zdania (składnia Assista), odpowiedź,
skrypt i reakcję robota. Są sprawdzane w integracji przed listą „Zostaw Vectorowi” i przed Assistem —
w wire-podzie nie trzeba nic zmieniać (`ha_router` i tak przekazuje każde zdanie).

### LLM wire-poda → Home Assistant
Przełącznik „LLM wire-poda → Home Assistant” kieruje LLM wire-poda (to, co wire-pod robi ze zdaniami,
których nikt nie obsłużył — pytania w rodzaju „jaka jest stolica Francji”) do aplikacji:
`http://<HA host IP>:8790/v1` (format OpenAI, token jako klucz). Odpowiada agent wybrany w opcjach
integracji. Własne ustawienia LLM wire-poda (dostawca, adres, klucz, model) są zapisywane w `/data`
i przywracane po wyłączeniu przełącznika; prompt i cała reszta pozostają nietknięte.

### Rozwiązywanie problemów
Dziennik aplikacji pokazuje każdą decyzję, np.
`voice router text="włącz światło w salonie" handled=true took=552ms`;
dziennik wire-poda pokazuje `Intent matched: intent_system_noaudio` dla obsłużonych zdań.

## Pulpit: karta sterowania
Integracja zawiera kartę Lovelace (nie trzeba dodawać zasobu) — kamera na żywo i sterowanie
„przytrzymaj, aby jechać”:

```yaml
type: custom:vector-control-card
camera: camera.vector
# speed: 120      # mm/s do przodu (opcjonalnie)
# hold: true      # przejmij kontrolę, gdy karta jest otwarta (domyślnie)
```

- Strzałki (do przodu, do tyłu, obrót), głowa i podnośnik działają, dopóki je przytrzymujesz: karta
  powtarza `vector.move` co 0,3 s, a Vector zatrzymuje się 0,7 s po ostatnim wywołaniu (także gdy
  zniknie sieć lub strona).
- Dopóki karta jest otwarta, utrzymuje kontrolę zachowań (Vector czeka na Twoje komendy; odnawiane co
  5 minut); zamknięcie karty oddaje kontrolę. Przycisk „Trzymaj” przełącza to zachowanie.
- Na ładowarce pasek proponuje „Zjedź z ładowarki”; strzałka do przodu robi to samo.
- Zamknięcie karty zatrzymuje kamerę.

Ta sama karta w okienku ([browser_mod](https://github.com/thomasloven/hass-browser_mod)) otwieranym
z kafelka, z kilkoma zachowaniami pod kamerą:

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

Identyfikatory encji zależą od języka Home Assistant w chwili dodawania integracji (np. polska
instalacja ma `sensor.vector_bateria`, `button.vector_tancz`) — sprawdź je w Ustawienia → Urządzenia.

## Ograniczenia
- Brak obrazów na ekranie twarzy: na Vectorze 2.0 (firmware 2.0.1.6076ep) wywołanie SDK
  `DisplayFaceImageRGB` powoduje awarię robota (błąd 914/915, potem restart) — serwer SDK robota
  zakłada ekran Vectora 1.0.
- Kamera daje ~2 klatki na sekundę.

## Konfiguracja
| Opcja | Opis |
|---|---|
| `wirepod_url` | Adres wire-poda, np. `http://192.168.1.20:8080` (nazwy `.local` mogą nie być rozwiązywane wewnątrz aplikacji). |
| `esn` | Numer seryjny robota; puste = pierwszy robot w wire-podzie. |
| `name` | Nazwa robota w Home Assistant. |
| `robot_ip`, `guid` | Nadpisują to, co zgłasza wire-pod (zwykle puste). |
| `voice_router` | Początkowy stan routera głosowego (później decyduje przełącznik w Home Assistant). |
| `hook_host` | Adres, pod którym wire-pod łączy się z aplikacją; puste = IP hosta Home Assistant. |
| `log_level` | `debug` zapisuje w dzienniku każde zdarzenie robota. |

Po uruchomieniu aplikacja zgłasza się integracji (wykrywanie przez Supervisora) — wykryta integracja
Vector pojawia się w **Ustawienia → Urządzenia oraz usługi**. Token API znajduje się w `/data/ws_token`.

## API (dla integracji)
`GET /ws` (nagłówek `Authorization: Bearer <token>`):
- `{"type":"hello","version","api":1,"robots":[state],"voice_router":{…}}` po połączeniu,
- `{"type":"event","event":"state","robot":"<esn>","state":{…}}` — pełny stan,
- `{"type":"event","event":"<name>","robot":"<esn>","data":{…}}` — `wake_word` (`phase` begin/end),
  `touched`, `picked_up`, `cliff`, `button_pressed`, `face_seen` (`name`, `face_id`), `intent`,
  `cube_tapped`, `cube_connected`, `cube_disconnected`, `control_lost`, `photo_taken`, `audio_started`,
  `audio_finished`, `speech` (`id`, `text`), `llm` (`id`, `text`), `voice_router`,
- komenda `{"id":1,"command":"…","robot":"<esn>","args":{…}}` → `{"type":"result","id":1,"success":…}`.

Komendy: `say_text {text, engine}` (`engine`: `wirepod` — domyślnie, TTS wire-poda bez kontroli
zachowań; `vector` — wbudowany angielski głos Vectora), `play_animation {name, loops}`,
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
`llm_reply {id, text}`, `refresh`, `move {left, right, head, lift}` (mm/s, rad/s; Vector zatrzymuje się
0,7 s po ostatniej), `stop_audio`, `set_audio_volume {volume}`, `list_photos`, `delete_photo {id}`,
`camera_snapshot`.

HTTP (ten sam token): `GET /snapshot[/<esn>]?cached=true` (ostatnia klatka, 404 gdy jej brak) lub
`?max_age=<s>` (świeża, uruchamia kamerę), `GET /mjpeg[/<esn>]` (multipart JPEG),
`GET /photo/<esn>/<id>[?thumb=true]`, `POST /audio[/<esn>]?volume=&interrupt=true` (plik audio w treści
żądania, odpowiedź 202).
