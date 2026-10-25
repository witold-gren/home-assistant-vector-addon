🇬🇧 [English version](README.md)

# Anki Vector dla Home Assistant — aplikacja (dodatek)

Steruj robotem [Anki Vector](https://www.digitaldreamlabs.com/) i obserwuj go z poziomu Home Assistant.
To repozytorium zawiera **aplikację** (dawniej dodatek): `vector-bridge`, małą usługę w Go, która
utrzymuje połączenie z robotem przez jego SDK i komunikuje się z
[integracją Vector](https://github.com/witold-gren/home-assistant-vector-integration).
Robot nadal działa ze swoim serwerem [wire-pod](https://github.com/kercre123/wire-pod).

Co zyskujesz razem z integracją:
- stan robota, zdarzenia i statystyki jako encje Home Assistant,
- zachowania, jazdę (także karta sterowania „przytrzymaj, aby jechać” z podglądem kamery na żywo), głowę, podnośnik, animacje,
- kamerę, zdjęcia, głośnik dla `tts.speak`,
- głos → Home Assistant: „Hey Vector, włącz światło” obsługuje Assist, a Vector odpowiada,
- własne komendy głosowe, LLM wire-poda obsługiwany przez agenta Home Assistant.

## Instalacja
1. **Ustawienia → Aplikacje → Sklep z aplikacjami → ⋮ → Repozytoria**, dodaj
   `https://github.com/witold-gren/home-assistant-vector-addon`.
2. Zainstaluj **Vector (Anki / Digital Dream Labs)**, ustaw `wirepod_url` (np. `http://192.168.1.20:8080`)
   i uruchom aplikację. Mostek jest kompilowany podczas instalacji (na Raspberry Pi trwa to kilka minut).
3. Zainstaluj [integrację Vector](https://github.com/witold-gren/home-assistant-vector-integration)
   (własne repozytorium HACS) — zostanie wykryta automatycznie.

Dokumentacja: [vector/DOCS.pl.md](vector/DOCS.pl.md) ([English](vector/DOCS.md)).

## Struktura repozytorium
- `vector/` — aplikacja: `config.yaml`, `Dockerfile` (buduje mostek), `run.sh`, tłumaczenia, dokumentacja,
- `vector/bridge/` — kod źródłowy mostka (Go): `cmd/vector-bridge`, narzędzia `cmd/vector-probe` (badanie
  robota) i `cmd/vector-ws` (klient API), notatki z prawdziwego robota: [FINDINGS.md](vector/bridge/FINDINGS.md) (po angielsku),
- `build.sh` — vet, testy i budowanie; `deploy.sh` — kopiowanie do zamontowanego udziału `addons` na potrzeby rozwoju,
- `tools/draw_icon.py` — rysuje ikonę i logo.

## Rozwój
```sh
./build.sh                       # go vet, go test -race, go build
cd vector/bridge && go run ./cmd/vector-bridge -wirepod http://<wire-pod>:8080 -hook-listen ""
```

## Licencja
MIT
