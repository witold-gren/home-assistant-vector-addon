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
  (ponownym) połączeniu — nie trzeba niczego przepisywać ręcznie. Jeśli ustawisz **oba** `robot_ip` i `guid`
  (oraz `esn`), aplikacja w ogóle nie pyta o nie wire-poda (patrz „Połączenie bez pytania wire-poda”).
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

## Kamera, mikrofon i zdjęcia
- **Podgląd na żywo** (opcja `live_stream`, domyślnie włączona): aplikacja uruchamia
  [go2rtc](https://github.com/AlexxIT/go2rtc) i udostępnia kamerę jako prawdziwe wideo — H.264 —
  przez RTSP wewnątrz sieci Home Assistant (port 8554 aplikacji, nie jest
  wystawiony na hoście; użytkownik `vector`, hasło = token API). Integracja przekazuje ten adres do Home
  Assistant, którego własne go2rtc zamienia go na WebRTC dla przeglądarki i aplikacji. Nic nie działa,
  dopóki nikt nie ogląda: go2rtc uruchamia kamerę i mikrofon przy pierwszym widzu i zatrzymuje je
  (SIGTERM, więc kamera robota zamyka się porządnie) kilka sekund po wyjściu ostatniego.
- Kamera z SDK robota daje około **2 obrazów na sekundę** (800×600). Strumień powtarza ostatni obraz
  z częstotliwością 10 kl./s, więc odtwarzacze startują szybko i grają płynnie; ruch nadal odświeża się
  mniej więcej dwa razy na sekundę. Pierwszy obraz pojawia się ok. 2–4 s po dotknięciu ▶ (na ładowarce
  dłużej, patrz niżej).
- **Bez dźwięku z Vectora:** strumień audio SDK to w firmware Vectora testowy ton 1 kHz, a nie mikrofon,
  więc podgląd domyślnie nie ma dźwięku (`stream_audio: false`; patrz FAQ). Żeby mówić do Vectora,
  przytrzymaj 🎙 na karcie sterowania: nagranie zostanie odtworzone na jego głośniku.
- Poza ładowarką Vector zajmuje się swoimi sprawami; **na ładowarce** klatki przychodzą tylko z kontrolą
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
- Kontrola zachowań jest przejmowana na czas sterowania: widok pełnoekranowy trzyma ją, dopóki jest
  otwarty, kafelek od pierwszej jazdy do minuty po ostatniej; wraca do Vectora po zamknięciu widoku
  albo gdy strona przejdzie w tło. Przycisk „Trzymaj” utrzymuje ją do ponownego naciśnięcia.
- Na ładowarce pasek proponuje „Zjedź z ładowarki”; strzałka do przodu robi to samo.
- ▶ uruchamia podgląd na żywo (WebRTC z dźwiękiem, gdy podgląd na żywo jest włączony, w przeciwnym
  razie MJPEG); ✕, 5 minut bez dotknięcia albo przejście strony w tło go zatrzymują. Zamknięcie karty
  zatrzymuje kamerę.
- ⛶ otwiera sterowanie na pełnym ekranie: poziomo, obraz z kamery wypełnia ekran, z lewej krzyżak
  jazdy, z prawej słuchanie (🔊 — mikrofon Vectora), głowa i ramię, na dole prędkość i ładowarka.

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

## Robots Code Studio (programy dzieci)
Robots Code Studio to edytor klocków / Pythona dla dzieci. Od aplikacji 0.3.0 może wykonywać program
dziecka na Vectorze krok po kroku: studio wykonuje program i wysyła aplikacji jedną komendę na klocek,
w ramach **sesji programu**. W czasie programu:

- aplikacja trzyma kontrolę zachowań Vectora przez cały program (Vector nie odjedzie na ładowarkę
  w środku klocka „czekaj 3 s”);
- karta Home Assistant i automatyzacje nie mogą ruszać Vectorem, odtwarzać animacji ani mówić przez
  niego — dostają błąd `program running`, a karta pokazuje „Program dziecka działa”. **Stop działa
  zawsze**: Stop na karcie (albo komenda `stop`, skrypt, automatyzacja) od razu kończy program;
- jeśli studio przestanie odpowiadać na kilka sekund (zamknięty laptop, zerwane Wi-Fi), aplikacja
  zatrzymuje silniki i oddaje Vectorowi swobodę (watchdog);
- sesja trwa najwyżej `limit` sekund (domyślnie 5 minut, najwyżej 30);
- jeśli Vector sam odbierze kontrolę — zobaczył **krawędź stołu**, ktoś go **podniósł** — program się
  kończy, a studio mówi dziecku dlaczego. To celowe: własne odruchy Vectora go chronią.

### Włączenie
1. W **Konfiguracji** aplikacji ustaw **Token studia** (`studio_token`) na wybrane hasło i zapisz.
   Puste = studio nie może się połączyć.
2. W sekcji **Sieć** aplikacji wpisz port przy `3012/tcp` (np. `3012`), zapisz i uruchom aplikację
   ponownie. Domyślnie port 3012 jest dostępny tylko wewnątrz Home Assistant (tam używa go integracja).
3. W studiu wpisz `ws://<adres Home Assistant>:3012/ws` i token studia.

Token studia otwiera tylko to, czego potrzebuje program: ruch, mowę, animacje, kolor oczu, kostkę,
czujniki, stan i (od 0.4.0) podgląd kamery tylko do odczytu. Nie zmieni ustawień, nie zmieni nazw ani
nie usunie twarzy, nie odczyta ani nie usunie zapisanych zdjęć, nie otworzy mikrofonu, nie przełączy
routera głosu. Komendy ruchu z tokenem studia wymagają też własnej sesji programu studia. Integracja ma
swój token (`/data/ws_token`).

**Podgląd kamery dla studia (aplikacja 0.4.0+).** Token studia (i token rodzica) może też *oglądać*
kamerę: `GET /mjpeg[/<esn>]` i `GET /snapshot[/<esn>]` z `Authorization: Bearer <token studia>` (albo
`?token=`), żeby studio pokazało dziecku, co widzi Vector. Tylko odczyt: bez `/mic`, bez zapisanych
zdjęć (`/photo`, `list_photos`, `delete_photo`), bez ustawień kamery — te odpowiadają **403**. Studio
powinno pobierać podgląd po swojej stronie serwera i przekazywać obrazy dalej (token nie trafia do
przeglądarki). Sesja programu nie jest potrzebna. Oglądanie działa jak na karcie: **poza ładowarką**
obraz przychodzi od razu, bez kontroli zachowań; **na ładowarce** kamera Vectora bez kontroli nic nie
wysyła, więc aplikacja bierze kontrolę, dopóki ktoś ogląda (Vector stoi w miejscu) — w czasie programu
i tak ją ma. Gdy działa `run_behavior`, aplikacja **nie** bierze kontroli dla kamery (przerwałoby to
zachowanie): na ładowarce obraz jest wtedy czarny do końca zachowania.

### Głosy, słuchanie, zachowania Vectora, AI i pogoda (aplikacja 0.4.0+)
Programy mogą też mówić wybranym głosem i ze śmiesznym efektem, czekać na to, co powie dziecko,
pozwolić Vectorowi zrobić jedną z jego sztuczek (taniec, przyjdź, znajdź kostkę…), zadać pytanie
lokalnemu AI, powiedzieć, jaka jest pogoda, i zmienić głośność. Ustawia się to raz:

1. **Głosy** — opcja `piper_url`: serwer Wyoming Piper, którego już używa Home Assistant, np.
   `tcp://192.168.1.20:10200`, i opcjonalnie `piper_voice` (np. `pl_PL-gosia-medium`). Aplikacja sama
   rozmawia z Piperem (protokół Wyoming), zamienia zdanie na WAV i odtwarza je na głośniku Vectora.
   Puste = programy mówią przez `say_text` wire-poda jego głosem (bez wyboru głosu i efektów).
2. **Słuchanie** — wymaga routera głosu (wystawiony port `8790/tcp`, wire-pod osiągalny): Vector wysyła
   to, co usłyszy po „Hey Vector”, tylko do wire-poda, a wire-pod oddaje każde zdanie tej aplikacji.
   Gdy działa program rozpoczęty z `claim_speech: true`, te zdania trafiają **tylko do programu**:
   Home Assistant ich nie widzi, a Vector milczy. Działa to także przy wyłączonym przełączniku
   „Głos → Home Assistant” (aplikacja trzyma swoją intencję w wire-podzie na czas programu).
3. **AI dla dzieci** — opcje `ai_url` (lokalny serwer [Ollama](https://ollama.com), np.
   `http://192.168.1.30:11434`) i `ai_model`. Puste `ai_url` = wyłączone. Każde pytanie to nowa
   rozmowa (bez historii), ze stałym poleceniem systemowym dla dzieci 7–12 lat: po polsku, najwyżej
   `max_words` słów, bez linków, e-maili i numerów telefonów, bez pytań o dane dziecka, a przy
   nieodpowiednich tematach „zapytaj rodzica”; linki, markdown i emoji są też usuwane z odpowiedzi.
   Aplikacja nie zapisuje pytań ani odpowiedzi (także w logu). AI tylko zwraca tekst — nigdy nie steruje
   robotem; to program decyduje, czy go powiedzieć (`say_voice`).
   **Jaki model:** model *instruct* (bez „myślenia”) 4–8B dobrze mówiący po polsku, np.
   `qwen3-vl:8b-instruct`, `gemma3:4b` albo Bielik 4.5B/7B. Unikaj modeli 20B+ (wolne, dzielą GPU z
   rozpoznawaniem mowy) i modeli „myślących” (długie przerwy). Pierwsze pytanie po przerwie ładuje
   model (do ~20 s); kolejne odpowiedzi trwają 1–5 s.
4. **Pogoda** — aplikacja ma dostęp tylko do odczytu do Home Assistant (`homeassistant_api`); opcja
   `weather_entity` (np. `weather.dom`), puste = pierwsza encja `weather.*`.
5. **Zdjęcia i nauka twarzy** — tylko dla konta rodzica w studiu: opcja `studio_parent_token`,
   **drugie, inne** hasło. Studio używa go tylko, gdy program uruchamia rodzic; dziecięcy
   `studio_token` nigdy nie zrobi zdjęcia ani nie nauczy twarzy. Puste = nigdy ze studia. Zdjęcie trafia
   do studia jako obraz w odpowiedzi na komendę i nie jest nigdzie zapisywane.

#### Co wymaga wire-poda, a co nie
| Funkcja | Potrzebny wire-pod? |
|---|---|
| Połączenie z Vectorem | Tylko do odczytu IP i GUID — nie, gdy ustawiono `robot_ip` + `guid` |
| `say_voice` / `list_voices` z `piper_url` | Nie (prosto do Pipera) |
| `say_text` (silnik `wirepod`), `say_voice` bez Pipera albo gdy Piper zawiedzie | Tak (`/api-sdk/say_text`) |
| `listen`, `claim_speech`, „gdy usłyszę…” | **Tak** — Vector wysyła mikrofon tylko do wire-poda (rozpoznawanie mowy) |
| `listen {wake: true}` | Tak (`/api-sdk/trigger_wake_word`; może nie działać na firmware produkcyjnym) |
| `ask_ai` | Nie (prosto do Ollamy; LLM wire-poda nie jest używany) |
| `weather` | Nie (Home Assistant) |
| `run_behavior`, `set_volume`, `mirror_mode`, `take_photo`, `sensors` | Nie (SDK robota) |

#### Połączenie bez pytania wire-poda
Ustaw raz `esn`, `robot_ip` i `guid` — wtedy aplikacja łączy się z Vectorem bezpośrednio. Skąd je
wziąć: strona wire-poda `http://<wire-pod>:8080/api-sdk/get_sdk_info` pokazuje dla każdego robota
`esn`, `ip_address` i `guid` (puste `guid` = użyj `global_guid`); diagnostyka integracji też pokazuje
ESN i IP robota. Daj Vectorowi stały adres IP w routerze — przy ręcznie wpisanym IP aplikacja nie
podąży za nowym adresem. wire-pod nadal jest potrzebny do słuchania i do głosu wire-poda.

### Jednostki
Aplikacja używa jednostek Vectora: milimetry, mm/s, stopnie, a **dodatni kąt obraca w lewo** (konwencja
Vectora). Studio pokazuje dzieciom centymetry i „plus = w prawo” i samo przelicza.

## FAQ
**Obraz z kamery wygląda jak pokaz slajdów — nowe zdjęcie co sekundę.**
Aplikacja jest starsza niż 0.2.0 (albo `live_stream` jest wyłączone), więc karta może tylko przeładowywać
klatki JPEG. Zaktualizuj aplikację i integrację do 0.2.0 lub nowszej i dotknij ▶ na karcie: encja kamery
ma wtedy prawdziwy strumień (WebRTC). Sama kamera Vectora nadal daje około 2 nowych obrazów na sekundę.

**Karta pokazuje ikonę zepsutego obrazka na napisie „Vector jest niedostępny”, a status „unavailable %”.**
Robot nie jest połączony (log aplikacji pokazuje `robot disconnected … no route to host`: Vector jest
wyłączony, rozładowany albo poza Wi-Fi). Integracja 0.2.0 pokazuje wtedy porządny ekran „Vector jest
niedostępny” i „—”; włącz Vectora albo postaw go na ładowarce, a karta wróci sama.

**Widok pełnoekranowy jest czarny, choć kafelek pokazuje obraz.**
Odtwarzacz kamery w Home Assistant 2026 bierze ustawienia z wnętrza strony Home Assistant; widok
pełnoekranowy był umieszczany poza nią, więc odtwarzacz nic nie rysował. Naprawione w integracji
0.2.2. Zaktualizuj ją i odśwież stronę.

**Podgląd staje albo w ogóle nie rusza; w logu HA `Error from stream worker … Invalid data found`
albo go2rtc `i/o timeout`.**
go2rtc w Home Assistant czeka na strumień tylko 5 s, a kamera Vectora na ładowarce potrzebuje
dłużej. Od aplikacji 0.2.2 i integracji 0.2.2 integracja najpierw prosi aplikację o uruchomienie
strumienia (log: `live stream warm`) i dopiero wtedy go otwiera.

**Podgląd kamery w studiu jest czarny, gdy Vector stoi na ładowarce.**
Na ładowarce kamera Vectora daje obraz tylko pod kontrolą zachowań. Aplikacja sama bierze kontrolę dla
kamery (Vector stoi wtedy w miejscu), z wyjątkiem chwili, gdy program uruchomił własne zachowanie
Vectora (`run_behavior`) — wtedy obraz jest czarny do końca zachowania. Jeśli jest czarny w innej
sytuacji: kamera robota zawiesiła się po drzemce na ładowarce (log: `camera: no frames even with
behavior control`) — uruchom Vectora ponownie. HTTP 403 na `/mic` albo `/photo` z tokenem studia jest
oczekiwany (studio ma dostęp tylko do podglądu kamery).

**Z podglądu słychać tylko piski.**
Firmware Vectora nigdy nie podłączył strumienia audio SDK do mikrofonów: wysyła testowy ton 1 kHz
(`sdkComponent.cpp`: „temporary 1 kHz sine wave generator to test over the wire transfer”). Przez SDK
nie da się więc słuchać Vectora; `stream_audio` jest domyślnie wyłączone. W drugą stronę działa:
przytrzymaj 🎙 na karcie, a Vector odtworzy Twój głos (integracja 0.2.3+).

**Program dziecka sam się zatrzymał („Vector zobaczył krawędź” / „Vector został podniesiony”).**
Własne odruchy bezpieczeństwa Vectora odebrały kontrolę: czujnik krawędzi zobaczył brzeg stołu albo
ktoś go podniósł (zdarzenie `program_ended` z `reason: control_lost`, `cause: cliff | picked_up |
falling | wake_word | robot`). Nie da się tego wyłączyć (wyłączyłoby to też odruch krawędzi). Postaw
Vectora na podłodze albo dalej od krawędzi i uruchom program jeszcze raz. Inne powody: `watchdog` —
studio przestało odpowiadać; `limit` — limit czasu; `ha_stop` — Stop w Home Assistant;
`disconnected` — zniknął robot albo połączenie studia.

**Studio nie łączy się z Vectorem.**
Sprawdź po kolei: port `3012/tcp` ma numer w sekcji **Sieć** aplikacji (puste = tylko wewnątrz Home
Assistant) i aplikacja została uruchomiona ponownie; studio używa `ws://<IP Home Assistant>:<ten
port>/ws`; opcja **Token studia** jest ustawiona, a studio używa dokładnie tej wartości (HTTP 401 = zły
albo pusty token). Gdy działa, log aplikacji pokazuje `studio connected`. Odpowiedź `not allowed with
the studio token` oznacza komendę, której studio nie może wysłać (ustawienia, twarze, zapisane zdjęcia).

**Karta Home Assistant nic nie robi, gdy działa program dziecka.**
To blokada programu: w czasie programu Vectorem steruje tylko program. Karta pokazuje „Program dziecka
działa” z wyszarzonym sterowaniem; dotknij **Stop**, żeby zakończyć program i odzyskać kartę.
Komunikaty Home Assistant przez głośnik Vectora też czekają (`POST /audio` odpowiada 409).

**„Zapytaj AI” odpowiada `ai is off (set the add-on option ai_url)`.**
Ustaw `ai_url` (np. `http://192.168.1.30:11434`) i `ai_model` i uruchom aplikację ponownie. Log pokazuje
`ask_ai: local LLM`, gdy AI działa (albo `ask_ai off` z powodem, gdy opcja jest błędna).

**„Zapytaj AI” mówi `ai: no answer within 45s` albo `ai: HTTP 404 model '…' not found`.**
Pierwsze pytanie ładuje model; duży model na zajętej maszynie może potrzebować więcej niż 45 s — użyj
mniejszego (4–8B). `not found`: nazwy z `ai_model` nie ma na serwerze Ollama (`ollama list` pokazuje
nazwy). `connection refused`: Ollama nie jest osiągalna z Home Assistant (domyślnie słucha tylko na
localhost: ustaw `OLLAMA_HOST=0.0.0.0`).

**Klocek pogody mówi `weather needs Home Assistant` albo `no weather entity`.**
Aplikacja potrzebuje `homeassistant_api` (włączone od 0.4.0 — zaktualizuj i uruchom ją ponownie) oraz
encji `weather.*`. Ustaw `weather_entity`, jeśli masz kilka; `… is unavailable` oznacza, że sama
integracja pogody nie ma teraz danych.

**„Hey Vector” w czasie programu dziecka nie włącza światła.**
Tak ma być: program, który słucha (`claim_speech`), zabiera każde zdanie po „Hey Vector”, więc słowa
dziecka nigdy nie przełączają niczego w domu, a Vector sam na nie nie odpowiada. Home Assistant znów
dostaje zdania zaraz po końcu programu (albo po Stop).

**Program mówi głosem wire-poda zamiast wybranego.**
`say_voice` przechodzi na wire-poda, gdy Piper jest nieosiągalny (odpowiedź `fallback: true` z błędem).
Sprawdź `piper_url` (`tcp://<host>:10200`) i czy nazwa głosu jest w `list_voices`.

**`listen` nigdy nic nie słyszy.**
Słuchanie wymaga routera głosu: wystawiony port `8790/tcp` i w logu linia `Voice → HA: hook …`.
Dziecko musi najpierw powiedzieć „Hey Vector” (albo program używa `wake: true`, które nie działa na
każdym firmware). `program_begin` odpowiada `claim_speech: false`, gdy routera głosu nie ma.

**`run_behavior` odpowiada `reason: not_started`.**
Vector nie podjął zachowania (był zajęty, przy niektórych zachowaniach stał na ładowarce albo jego
firmware nie przyjmuje tej intencji z SDK). Program idzie dalej; spróbuj ponownie poza ładowarką.

**Podgląd na żywo nie ma dźwięku.**
Przeglądarki odtwarzają dźwięk dopiero po dotknięciu: otwórz widok pełnoekranowy (⛶) i dotknij 🔊. Jeśli 🔊
mówi, że dźwięk wymaga nowszej aplikacji, zaktualizuj ją do 0.2.0 lub nowszej i sprawdź, czy
`stream_audio` jest włączone. Jeśli dźwięk jest za szybki lub za wolny, ustaw `mic_sample_rate` na wartość,
którą log aplikacji pokazuje jako `sample_rate_hz`.

## Ograniczenia
- Brak obrazów na ekranie twarzy: na Vectorze 2.0 (firmware 2.0.1.6076ep) wywołanie SDK
  `DisplayFaceImageRGB` powoduje awarię robota (błąd 914/915, potem restart) — serwer SDK robota
  zakłada ekran Vectora 1.0.
- Kamera daje ~2 nowe obrazy na sekundę (strumień jest płynny, ale ruch odświeża się tak często).
- Brak świateł na grzbiecie: SDK nie ma do nich wywołania. Mikrofonu nie da się odczytać (ton testowy).
- `drive_on_charger` nie szuka ładowarki: działa tylko, gdy Vector ją widzi.

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
| `live_stream` | Prawdziwe wideo (go2rtc → WebRTC w Home Assistant) zamiast samego MJPEG. Domyślnie włączone. |
| `stream_audio` | Dodaje do strumienia audio z SDK. Domyślnie wyłączone: firmware Vectora wysyła tam testowy ton 1 kHz, a nie mikrofon (patrz FAQ). |
| `mic_sample_rate` | Częstotliwość mikrofonu (domyślnie 12000, zmierzone na Vectorze 2.0); wpisz wartość z logu, jeśli dźwięk jest za szybki lub za wolny. |
| `studio_token` | Hasło dla Robots Code Studio (programy dzieci); puste = bez studia. Wystaw też port 3012 (patrz Robots Code Studio). |
| `studio_parent_token` | Drugie, inne hasło, którego studio używa dla konta rodzica: także `take_photo` i `enroll_face`. Puste = nigdy ze studia. |
| `piper_url`, `piper_voice` | Serwer Wyoming Piper (`tcp://host:10200`) i domyślny głos dla `say_voice`; puste = mówi wire-pod. |
| `ai_url`, `ai_model` | Lokalny serwer Ollama i model dla `ask_ai`; puste = wyłączone. |
| `weather_entity` | Encja pogody dla `weather`; puste = pierwsza `weather.*`. |

Po uruchomieniu aplikacja zgłasza się integracji (wykrywanie przez Supervisora) — wykryta integracja
Vector pojawia się w **Ustawienia → Urządzenia oraz usługi**. Token API znajduje się w `/data/ws_token`.

## API (dla integracji)
`GET /ws` (nagłówek `Authorization: Bearer <token>`):
- `{"type":"hello","version","api":1,"features":["program"],"program":null,"robots":[state],"voice_router":{…},"stream":{"rtsp_port":8554,"path":"vector","username":"vector","audio":true}}`
  po połączeniu (`stream` tylko przy włączonym podglądzie na żywo; `program` = `{owner, since, limit}`,
  gdy działa program; `"role":"studio"` dla połączenia studia),
- `{"type":"event","event":"state","robot":"<esn>","state":{…}}` — pełny stan,
- `{"type":"event","event":"<name>","robot":"<esn>","data":{…}}` — `wake_word` (`phase` begin/end),
  `touched`, `picked_up`, `cliff`, `button_pressed`, `face_seen` (`name`, `face_id`), `intent`,
  `cube_tapped`, `cube_connected`, `cube_disconnected`, `control_lost`, `photo_taken`, `audio_started`,
  `audio_finished`, `speech` (`id`, `text`), `llm` (`id`, `text`), `voice_router`, `program_started`
  (`owner`, `since`, `limit`, `heartbeat`), `program_ended` (`owner`, `reason`, `cause`, `duration`),
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

HTTP (ten sam token; `/snapshot` i `/mjpeg` także z tokenami studia, reszta odpowiada im 403): `GET /snapshot[/<esn>]?cached=true` (ostatnia klatka, 404 gdy jej brak) lub
`?max_age=<s>` (świeża, uruchamia kamerę), `GET /mjpeg[/<esn>]` (multipart JPEG),
`GET /mic[/<esn>]?rate=16000` (mikrofon robota jako surowy 16-bit mono PCM, cisza w przerwach),
`GET /photo/<esn>/<id>[?thumb=true]`, `POST /audio[/<esn>]?volume=&interrupt=true` (plik audio w treści
żądania, odpowiedź 202).

### Sesje programu (API, aplikacja 0.3.0+)
Sprawdź, czy `hello.features` zawiera `"program"` (starsze aplikacje go nie mają). Wszystkie komendy
poniżej idą tym samym WebSocketem; `session` to identyfikator zwrócony przez `program_begin` — nigdy nie
jest rozgłaszany.

| Komenda | Argumenty | Wynik |
|---|---|---|
| `program_begin` | `owner` (tekst, np. `"rcs"`), `limit` (s, 10–1800, domyślnie 300), `heartbeat` (s, 1–30, domyślnie 3), `drive_off_charger` (bool, domyślnie false) | `{session, limit, heartbeat, state, sensors}` |
| `program_heartbeat` | `session` | `{remaining, sensors}` (`remaining` = sekundy do limitu) |
| `program_end` | `session`, `reason` (opcjonalnie: `end`, `stop`, `error`…, domyślnie `end`) | `null` |
| `sensors` | (`session` opcjonalnie) | czujniki, od razu (niżej) |
| `set_wheels` | `session`, `left`, `right` (mm/s, ±220) | `null` — jedzie do `set_wheels 0 0`, `stop`, końca programu albo watchdoga |
| `set_cube_lights` | `color` (`"#rrggbb"`) albo `hue` + `saturation` (0–1), `mode` (`on` domyślnie, `flash`, `off`) | `null` (kostka musi być połączona) |

Błędy `program_begin`: `program running` (działa inna sesja), `robot not connected`, `battery low`
(poziom baterii low albo poniżej 10 %), `behavior control not granted`, `drive off charger: …`. Inne
komendy: `no such program session` (sesja się skończyła — patrz `program_ended`), `program session
required` (studio wysłało komendę ruchu bez swojej sesji).

- **Heartbeat:** wysyłaj `program_heartbeat` przynajmniej co `heartbeat / 2` sekund — także w trakcie
  czekania na długą komendę, np. `drive_straight`. Każda komenda z `session` też odnawia watchdog. Brak
  heartbeatu przez `heartbeat` sekund → silniki stają, bieżąca akcja jest anulowana, dźwięk aplikacji
  staje, kontrola jest zwalniana i wysyłane jest `program_ended {reason: "watchdog"}`.
- **W sesji** używaj zwykłych komend z `"session": "<id>"` w `args` (`drive_straight`, `turn_in_place`,
  `say_text`, `play_animation_trigger`, `set_head_angle`, …). Dźwięki: `POST /audio?session=<id>`.
- **Blokada:** w czasie sesji komendy ruchu / animacji / mowy / kontroli od innych (bez identyfikatora
  sesji i nie z połączenia, które ją zaczęło) kończą się błędem `program running`; odczyt (`refresh`,
  `sensors`, `list_*`), stan i zdarzenia dalej trafiają do wszystkich. `stop` działa zawsze i kończy
  program (`reason: "ha_stop"`; `"stop"`, gdy wysyła go sam program).
- **Powody końca** (`program_ended.reason`): powód programu (`end`, `stop`, `error`…), `watchdog`,
  `limit`, `ha_stop`, `control_lost` (+ `cause`: `cliff`, `picked_up`, `falling`, `wake_word`, `robot`,
  `connection`), `disconnected` (+ `cause`: `client` — zamknięty WebSocket programu, `robot` — Vector
  zniknął).

`sensors` (jednostki Vectora: mm, stopnie, mm/s; kąty + = w lewo):
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
Od aplikacji 0.4.0 `sensors` ma też `"stimulation": 0.4` (jak bardzo Vector jest podekscytowany, 0–1),
`"volume": 3` (głośność 0 wyciszony … 5 głośno; `null` = jeszcze nie odczytana) i `"life": {"alive_days":
1460.2, "pet_minutes": 312.5, "distance_m": 826.8, "wake_words": 2412}` (statystyki życia, odświeżane co
10 minut; pole jest `null`, gdy robot go nie podaje).

`obstacle_mm` to prawdziwa odległość tylko przy `obstacle_ok` = true (obiekt w zasięgu czujnika i podnośnik
nie zasłania czujnika); `face` to ostatnia widziana twarz (`age_s` ≤ 2 = widzi ją teraz; puste `name` =
nieznana twarz; `null` = jeszcze żadnej); `last_speech` to to, co wire-pod usłyszał po „Hey Vector”
(`null` = jeszcze nic); `counts` liczą od `program_begin` (albo od startu aplikacji, gdy nie ma programu).

Przykład:
```json
→ {"id":1,"command":"program_begin","args":{"owner":"rcs","limit":300,"heartbeat":3}}
← {"type":"result","id":1,"success":true,"result":{"session":"5f0c…","limit":300,"heartbeat":3,"state":{…},"sensors":{…}}}
← {"type":"event","event":"program_started","robot":"00a1b2c3","data":{"owner":"rcs","since":1795428977,"limit":300,"heartbeat":3}}
→ {"id":2,"command":"drive_straight","args":{"session":"5f0c…","distance":100,"speed":100}}
→ {"id":3,"command":"program_heartbeat","args":{"session":"5f0c…"}}
→ {"id":4,"command":"program_end","args":{"session":"5f0c…","reason":"end"}}
← {"type":"event","event":"program_ended","robot":"00a1b2c3","data":{"owner":"rcs","reason":"end","duration":12.3}}
```

### Komendy etapu V5 (aplikacja 0.4.0+): kontrakt dla studia
`hello.features` mówi, co ta aplikacja potrafi: zawsze `program`, `behavior`, `volume`, `mirror`;
`listen` z routerem głosu; `voices` z `piper_url`; `ai` z `ai_url`; `weather` z dostępem do Home
Assistant; `parent`, gdy ustawiono `studio_parent_token`. `hello.parent` = `true` dla połączenia z
tokenem rodzica. „Sesja” = komenda wymaga własnej sesji programu (`session` w `args`).

| Komenda | Argumenty | Wynik | Sesja | Token studia |
|---|---|---|---|---|
| `program_begin` (nowy argument) | + `claim_speech` (bool, domyślnie false) | + `claim_speech` (true tylko, gdy jest router głosu) | — | tak |
| `list_voices` | — | `{voices: [{name, description, languages, speakers}], effects: ["none","mouse","giant","robot","echo","fast","slow"]}` | nie | tak (wolna) |
| `say_voice` | `text` (≤300 znaków), `voice` (z `list_voices`, domyślnie `piper_voice`), `effect` (domyślnie `none`), `wait` (bool, domyślnie true), `volume` (0–100, opcjonalnie) | `{engine: "piper", duration}` (wait) / `{engine: "piper", queued}` / `{engine: "wirepod", fallback: true, error?}` | studio: tak | tak |
| `listen` | `timeout` (s, 2–30, domyślnie 8), `prompt` (≤200, mówione najpierw), `voice`, `wake` (bool, domyślnie false — Vector słucha bez „Hey Vector”; eksperymentalne) | `{heard, text, waited, woke}` (`heard: false`, `text: ""` po czasie) | tak, z `claim_speech` | tak |
| `run_behavior` | `behavior`: `dance`, `come_here`, `look_at_me`, `look_over_there`, `who_am_i`, `find_cube`, `fetch_cube`, `explore`, `go_to_charger`; `timeout` (s, 5–120; domyślnie 15–60 zależnie od zachowania) | `{behavior, finished, reason: finished\|not_started\|timeout, seconds, control}`; `go_to_charger`: `{behavior, program_ended: true, reason: "charger"}` | tak | tak |
| `ask_ai` | `question` (≤200 znaków), `max_words` (5–60, domyślnie 30) | `{answer, words, seconds, model}` | studio: tak | tak |
| `weather` | `entity` (opcjonalnie), `forecast_days` (0–5, domyślnie 0) | `{entity, condition, condition_pl, temperature, unit, humidity, wind_speed, wind_unit, text, forecast: [{date, condition, condition_pl, temperature, templow, precipitation}]}` | nie | tak (wolna) |
| `set_volume` | `level` (0 wyciszony … 5 głośno) | `null`; głośność sprzed pierwszej zmiany wraca na końcu programu | tak | tak |
| `mirror_mode` | `enable` (bool) | `null`; wyłączany na końcu programu | tak | tak |
| `take_photo` | — | `{image: "data:image/jpeg;base64,…", bytes, at}` | studio: tak | **tylko token rodzica** |
| `enroll_face` | `name` | `null` (Vector szuka twarzy i mówi imię) | studio: tak | **tylko token rodzica** |

Błędy (tekst `error`): `program session required` (brak własnej sesji albo komenda sesji z Home
Assistant), `listening needs program_begin with claim_speech`, `already listening`, `program ended`
(sesja skończyła się w czasie słuchania), `a Vector behavior is running` (każda komenda ruchu/mowy
właściciela, gdy `run_behavior` czeka), `behavior: one of …`, `stopped` (`stop` / koniec programu w
czasie `say_voice`, `run_behavior`), `parent permission required` (token dziecka), `not allowed with
the studio token` (komenda spoza listy), `no own voices (set the add-on option piper_url)`, `text too
long (max 300 characters)`, `effect: one of …`, `voice: a voice name from list_voices`, `ai is off (set
the add-on option ai_url)`, `ai: no answer within 45s`, `ai: HTTP …`, `ai: empty answer`, `question
required`, `question too long (max 200 characters)`, `weather needs Home Assistant (homeassistant_api)`,
`no weather entity in Home Assistant`, `<encja> is unavailable`, `control not back after the behavior:
…` (program kończy się wtedy z `control_lost`, `cause: behavior`).

Czasy: `say_voice` z Piperem ≈ synteza 0,2–1 s + długość zdania; zapasowy wire-pod też odpowiada po
wypowiedzeniu. `listen` wraca przy pierwszym zdaniu albo po `timeout` (+ prompt). `ask_ai` ≤ 45 s
(pierwsze pytanie ładuje model). `weather` ≤ 15 s. `set_volume` ≈ 1 s (ustawienia robota).
`run_behavior` ≤ `timeout` + ~1 s na odzyskanie kontroli; kończy się `not_started` po ~6 s, jeśli Vector
nie zaczął zachowania.

Sesja wokół `run_behavior`: aplikacja oddaje kontrolę (Vector potrzebuje jej do własnego zachowania),
zostawia blokadę programu (Home Assistant nadal dostaje `program running`), wstrzymuje watchdog,
wysyła intencję, obserwuje aktywność robota do końca zachowania (`finished`), upływu czasu (`timeout`)
albo gdy Vector go nie zaczął (`not_started`), i odzyskuje kontrolę. `stop` albo koniec programu w
czasie zachowania zatrzymuje silniki, ale Vector może dokończyć animację. `go_to_charger` najpierw
kończy program (`program_ended {reason: "charger"}`) — Vector potrzebuje własnej kontroli, żeby znaleźć
ładowarkę i na nią wjechać — więc to zawsze ostatni klocek programu.

Mowa programu: gdy działa sesja z `claim_speech`, każde zdanie trafia do `sensors.last_speech`,
`counts.speech` i do czekającego `listen`; zdarzenie `program_heard {chars}` niesie tylko długość
(tekst nie trafia do innych klientów). Home Assistant odzyskuje mowę na końcu programu.

