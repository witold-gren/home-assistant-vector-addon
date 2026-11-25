# Vector without Home Assistant

🇵🇱 [Polska wersja](README.pl.md)

Runs this bridge as a plain Docker container, only for **Robots Code Studio**:

`child's computer → Robots Code Studio → this container → robot`

Do not run it together with the Home Assistant add-on for the same robot: the robot accepts one of them.

## What you need
- **Linux** with Docker and Docker Compose (Raspberry Pi, NUC, PC; amd64 or arm64).
- A running Robots Code Studio that can reach this computer on port **3012**.

## Start
```sh
cd standalone
./setup.sh                    # creates data/options.json and a random token
nano data/options.json        # before the first start: options, see the table
docker compose up -d --build
curl http://127.0.0.1:3012/healthz
docker compose logs -f
```

## The robot
Vector uses **Wi-Fi, not Bluetooth**, so no Bluetooth is shared. It needs a running [wire-pod](https://github.com/kercre123/wire-pod) that already knows the robot. Without Home Assistant there is no weather and no voice → Home Assistant.

## Robots Code Studio
Show the token:
```sh
cd standalone && grep '"studio_token"' data/options.json
```
In the studio: **Settings → Robots → Vector**, address `ws://<address of this computer>:3012/ws` and the token. Use `studio_token` as the token. For the camera preview put the contents of `data/ws_token` in **camera_token** (the file appears after the first start). For photos and faces set `studio_parent_token` and give it as **parent_token**.
When the studio runs in Docker on the same computer, use the computer's LAN address, not `127.0.0.1`.

## Options
| Option | Meaning |
|---|---|
| `wirepod_url` | **required**: your wire-pod, e.g. `http://192.168.1.20:8080` |
| `esn`, `robot_ip` | which robot (when wire-pod knows several) and its address |
| `piper_url`, `ai_url` | voices and the local AI; empty = off |

The others are described in [DOCS.md](../vector/DOCS.md). After a change: `docker compose restart`. Without Home Assistant there is no automatic discovery and no joystick card.

## Update, stop, remove
```sh
git pull && docker compose up -d --build
docker compose down           # data/ with the token and the settings stays
```

## Safety
The connection is not encrypted. Keep port 3012 inside the home network and never forward it on the router. `data/` holds the secrets and is not in git.

## FAQ
| Problem | What to do |
|---|---|
| The studio says the token is wrong | Copy it again (above). It has to be the same in the studio and in `data/`. |
| Port already in use | The Home Assistant add-on or another container runs on this computer: stop one of them. |
