# Vector bez Home Assistant

🇬🇧 [English version](README.md)

Uruchamia ten mostek jako zwykły kontener Dockera, tylko dla **Robots Code Studio**:

`komputer dziecka → Robots Code Studio → ten kontener → robot`

Nie uruchamiaj go razem z dodatkiem Home Assistant dla tego samego robota: robot przyjmie tylko jednego z nich.

## Czego potrzebujesz
- **Linux** z Dockerem i Docker Compose (Raspberry Pi, NUC, PC; amd64 albo arm64).
- Działającego Robots Code Studio, które widzi ten komputer na porcie **3012**.

## Uruchomienie
```sh
cd standalone
./setup.sh                    # tworzy data/options.json i losowy token
nano data/options.json        # przed pierwszym startem: opcje, patrz tabela
docker compose up -d --build
curl http://127.0.0.1:3012/healthz
docker compose logs -f
```

## Robot
Vector działa przez **Wi-Fi, nie Bluetooth**, więc Bluetootha się nie udostępnia. Potrzebny jest działający [wire-pod](https://github.com/kercre123/wire-pod), który już zna robota. Bez Home Assistant nie ma pogody ani głosu → Home Assistant.

## Robots Code Studio
Pokaż token:
```sh
cd standalone && grep '"studio_token"' data/options.json
```
W studiu: **Ustawienia → Roboty → Vector**, adres `ws://<adres tego komputera>:3012/ws` i token. Jako token wpisz `studio_token`. Do podglądu kamery wpisz w **camera_token** zawartość `data/ws_token` (plik powstaje po pierwszym starcie). Do zdjęć i twarzy ustaw `studio_parent_token` i wpisz go jako **parent_token**.
Gdy studio działa w Dockerze na tym samym komputerze, podaj adres komputera w sieci domowej, nie `127.0.0.1`.

## Opcje
| Opcja | Znaczenie |
|---|---|
| `wirepod_url` | **wymagane**: twój wire-pod, np. `http://192.168.1.20:8080` |
| `esn`, `robot_ip` | który robot (gdy wire-pod zna kilka) i jego adres |
| `piper_url`, `ai_url` | głosy i lokalna SI; puste = wyłączone |

Pozostałe opisuje [DOCS.pl.md](../vector/DOCS.pl.md). Po zmianie: `docker compose restart`. Bez Home Assistant nie ma automatycznego wykrywania ani karty z joystickiem.

## Aktualizacja, zatrzymanie, usunięcie
```sh
git pull && docker compose up -d --build
docker compose down           # data/ z tokenem i ustawieniami zostaje
```

## Bezpieczeństwo
Połączenie nie jest szyfrowane. Port 3012 trzymaj w sieci domowej i nie przekierowuj go na routerze. W `data/` są sekrety; nie trafia do gita.

## FAQ
| Problem | Co zrobić |
|---|---|
| Studio pisze, że token jest zły | Skopiuj go jeszcze raz (wyżej). Musi być taki sam w studiu i w `data/`. |
| Port jest zajęty | Na tym komputerze działa dodatek Home Assistant albo inny kontener: zatrzymaj jeden z nich. |
