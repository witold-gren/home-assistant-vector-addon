# Anki Vector for Home Assistant — app (add-on)

🇵🇱 [Polska wersja](README.pl.md)

Control and watch an [Anki Vector](https://www.digitaldreamlabs.com/) robot from Home Assistant.
This repository holds the **app** (formerly add-on): `vector-bridge`, a small Go service that keeps the
robot connected through its SDK and talks to the
[Vector integration](https://github.com/witold-gren/home-assistant-vector-integration).
Your robot keeps working with its [wire-pod](https://github.com/kercre123/wire-pod) server.

What you get together with the integration:
- the robot's state, events and statistics as Home Assistant entities,
- behaviors, driving (also a hold-to-drive control card with live camera), head, lift, animations,
- a camera, photos, a speaker for `tts.speak`,
- voice → Home Assistant: "Hey Vector, turn on the light" handled by Assist, Vector answering,
- custom voice commands, wire-pod's LLM answered by a Home Assistant agent.

## Installation
1. **Settings → Apps → App store → ⋮ → Repositories**, add
   `https://github.com/witold-gren/home-assistant-vector-addon`.
2. Install **Vector (Anki / Digital Dream Labs)**, set `wirepod_url` (e.g. `http://192.168.1.20:8080`)
   and start it. The bridge is compiled during installation (a few minutes on a Raspberry Pi).
3. Install the [Vector integration](https://github.com/witold-gren/home-assistant-vector-integration)
   (HACS custom repository) — it is discovered automatically.

Documentation: [vector/DOCS.md](vector/DOCS.md).

## Repository layout
- `vector/` — the app: `config.yaml`, `Dockerfile` (builds the bridge), `run.sh`, translations, docs,
- `vector/bridge/` — the bridge source (Go): `cmd/vector-bridge`, tools `cmd/vector-probe` (robot
  research) and `cmd/vector-ws` (API client), notes from a real robot: [FINDINGS.md](vector/bridge/FINDINGS.md),
- `build.sh` — vet, tests and a build; `deploy.sh` — copy to a mounted `addons` share for development,
- `tools/draw_icon.py` — draws the icon and the logo.

## Development
```sh
./build.sh                       # go vet, go test -race, go build
cd vector/bridge && go run ./cmd/vector-bridge -wirepod http://<wire-pod>:8080 -hook-listen ""
```

## License
MIT
