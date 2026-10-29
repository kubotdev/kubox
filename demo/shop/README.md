# demo/shop — the example slice

A tiny three-service "shop" that exercises everything kubox can isolate:

```
orders (app) ──▶ inventory (dependency) ──▶ postgres (dependency, PVC)
```

- **`app`** — one Go binary, two roles (`DEMO_ROLE=orders|inventory`).
  Orders lists/creates orders; creation reserves stock via the inventory
  service; inventory decrements a postgres-backed stock table.
- **`inventory`** — the same image, run with a different env (one build, two
  services).
- **`postgres`** — stateful dependency with a PVC, so data survives restarts
  and re-syncs.

## Run it

```sh
kubox build demo/shop            # docker build + kind load (kind contexts)
kubox up demo/shop --name shop --ttl 1h
kubox test load shop --script demo/shop/tests/load.js
kubox test drill shop --all --json   # dependency-restart drill, PVC survives
kubox down shop
```

## The tests

- `tests/load.js` — sanity load: 10 VUs, p95 < 200 ms, error rate < 1 %.
- `tests/crush.js` — climbs to 800 VUs to break it; watch the envelope
  (`--report`) tell you *what* gave up (throttle, OOMKill, quota).

The app deliberately has no auth, no config, and no external calls —
everything interesting happens inside the environment.
