# Bitlite

| 🇺🇸 [English](README.md) | 🇮🇷 [فارسی](README-FA.md) |
|--------------------------|----------------------------|

Bitlite is a Stratum V1 proxy for SHA-256 ASIC miners with an embedded web admin UI, in one Go binary and one Docker container. Miners connect to the proxy over TCP or TLS. For each miner the proxy opens its own connection to the active pool, rewrites the worker login to the pool account, and moves the farm to another pool when you switch it or when the pool goes down.

On top of the proxy, Bitlite adds what a farm host needs when other people's miners run on it: customers and devices that you approve (with an end date), roles and an audit log, an optional and transparent operator share, and a Persian (right-to-left) interface and Telegram bot next to English and Russian.

Bitlite is a fork of [famfamfam/simple-mining-proxy](https://github.com/famfamfam/simple-mining-proxy), under the same GPL-3.0 license. Everything the original proxy does is kept.

It was written for BTC and BCH pools. Pools for XEC, DigiByte (SHA-256) and Fractal Bitcoin that speak standard Stratum V1 should work too: the proxy does not look inside jobs or blocks, it only relays Stratum messages.

## Features

- Stratum TCP and TLS listeners at the same time (`stratum+tls`, also written `stratum+ssl`).
- TCP or TLS to each pool, independent of how the miner connected.
- One pool connection per miner session.
- `mining.authorize` and the worker field of `mining.submit` are rewritten to the selected pool's account. All other submit fields, including version-rolling (ASICBoost) bits, pass through unchanged.
- An active pool and an ordered list of fallback pools, with health checks, failover and delayed failback.
- Several addresses per pool; new sessions go to the fastest working one.
- Pool switches reconnect miners gradually over a configurable window.
- Accepted and rejected shares, reject reasons and a 10-minute hashrate estimate, per pool and per miner.
- Charts of hashrate, shares and miners from one hour to one year, stored on disk, including a history for each miner.
- Optional profit switching between SHA-256 coins (BTC, BCH, BSV, XEC, DGB, FB) using WhatToMine and WhatsOnChain data: advice only, or automatic.
- Optional timed switching: part of every period on another pool, for example 10 minutes of every 30 on a solo pool. On eCash it waits out the minutes after a block, when the real-time target makes a solo block practically impossible.
- Optional block hunting on eCash: the farm mines on an XEC solo pool only while a block is no harder than usual and comes back the moment a block is found. It runs along with the timer and profit switching.
- Network difficulty and the farm's solo odds for every coin, with eCash's Real Time Targeting taken into account.
- Alerts when an ASIC stops sending shares and when it comes back: in the events and, with a bot set up in the admin UI, in Telegram, where `/status` gives a short summary of the farm.
- Runtime settings with descriptions and validation, applied without a restart.
- A self-signed TLS certificate on first start, or your own certificate, reloaded when it changes.
- Connection limits and timeouts against slow or broken clients.
- Admin UI in English, Russian and Persian (right to left) that works on a phone, with light and dark themes. Everything it does is also available through a REST API.

Added by Bitlite:

- Customers and devices: a miner logs in as `<customer>.<worker>` with the customer's connection key; a new device waits until you approve it, for a time you choose (a week, a month, ... or no end), and you can suspend, block, resume or revoke it. Blocking one device does not touch the customer's other devices. See [Customers and device control](#customers-and-device-control).
- Users with roles and permissions, and an audit log of who did what and when. See [Users, roles and the audit log](#users-roles-and-the-audit-log).
- An optional operator share of a customer's mining time, off by default, with the customer's recorded consent, a cap, and an account of every window. See [Operator share](#operator-share-optional).
- A check for the pool side of the operator share: does the pool accept a second worker on one connection. See [Checking a pool for the operator share](#checking-a-pool-for-the-operator-share).
- Persian interface and Telegram bot, with an optional Jalali (Persian) calendar for dates.

## Quick start

You need Docker Engine with Compose v2.

```bash
git clone https://github.com/clonerdev/simple-mining-proxy.git
cd simple-mining-proxy
cp .env.example .env
```

Set at least these in `.env`:

- `ADMIN_PASSWORD`: a long, unique password;
- `PUBLIC_HOST`: the IP address or DNS name your miners will connect to;
- `TLS_SELF_SIGNED_HOSTS`: the DNS names and IP addresses miners use for the TLS port.

Start it:

```bash
docker compose up -d --build
docker compose ps
docker compose logs -f proxy
```

Ports published by `docker-compose.yml`:

| Purpose | Default host address |
|---|---|
| Stratum TCP | `0.0.0.0:13333` |
| Stratum TLS | `0.0.0.0:14443` |
| Admin UI and API | `127.0.0.1:18080` |

Pools, settings, certificates and history are stored in the Docker volume `<project>_proxy-data` (`simpleminingproxy_proxy-data` with the project name from `.env.example`). Container logs are capped at five files of 10 MiB.

## Admin UI

The admin port listens on localhost only. Reach it through an SSH tunnel:

```bash
ssh -L 18080:127.0.0.1:18080 user@your-server
```

Open http://127.0.0.1:18080 and log in with `ADMIN_USERNAME` and `ADMIN_PASSWORD`. Sessions are kept in memory for 12 hours and end when the process restarts.

To reach the admin UI from the internet, change its port binding in `docker-compose.yml`, for example to `"18080:18080/tcp"`. The UI is plain HTTP, so the password and the session cookie travel unencrypted: put it behind an HTTPS reverse proxy, or keep using the tunnel. Failed logins are limited (password and API token failures count together):

| Rule | Value |
|---|---|
| Lock an IP address | after 5 failures within 15 minutes |
| IP lock duration | 15 min, then 30 min, 1 h and so on up to 24 h; a day without failures starts over |
| Lock password logins for everyone | after 100 failures from all addresses within an hour; existing sessions keep working |
| Successful login | clears the counter of that address |

The counters are saved in `/data/auth-guard.json` and survive restarts. Locks show up under Events. If you locked yourself out, wait, or delete the file:

```bash
docker compose stop proxy
docker run --rm -v simpleminingproxy_proxy-data:/d alpine rm -f /d/auth-guard.json
docker compose start proxy
```

## First setup

1. On the Dashboard, add the main pool: name, coin, addresses, login template and password. Pool names must be unique; for two pools of the same service add the coin, for example "EMCD BTC" and "EMCD BCH".

   Addresses takes one `host:port` per line, for servers of the same pool in different regions. The proxy measures the connect time to each (the median of the last probes), sends new sessions to the fastest working address and falls back to the next one if it fails. The fastest address is marked with ★.
2. Choose TLS if the pool requires it. Pasting an address with a scheme (`stratum+tcp://`, `stratum+tls://` or `stratum+ssl://`) selects the transport. Certificate verification is on by default; turn it off only for a known pool with a self-signed certificate.
3. Test checks the form before you save it: it connects, sends `mining.subscribe` and `mining.authorize` with the test worker, and waits for a usable `mining.notify`, on every address. Then save the pool and make it active.
4. Add fallback pools and set their order.
5. Review the test worker, timeouts and limits in Settings.
6. Copy the TCP or TLS address from the Dashboard into your miners:

```text
stratum+tcp://proxy.example.com:13333
stratum+tls://proxy.example.com:14443
```

`stratum+tls` and `stratum+ssl` are the same thing under two names. Whatsminer accepts only `stratum+tls`; some other firmware accepts only `stratum+ssl`.

## Worker names

A pool's login template may contain:

- `{worker}`: the part of the miner's login after the first dot;
- `{login}`: the miner's whole login;
- `{password}`: the password the miner sent.

With the template `account.{worker}`, a miner logged in as `farm.S21-0042` works on the pool as `account.S21-0042`.

Every valid `mining.authorize` that passes through the proxy is rewritten to the selected pool's login and password, and so is `params[0]` of every `mining.submit`. Firmware dev-fee traffic is not treated specially: a separate connection the firmware opens to its own pool does not pass through the proxy, and an authorize sent through the proxy is rewritten like any other.

## Firmware notes

| Firmware | Notes |
|---|---|
| Stock Antminer | Use the TCP port: stock firmware on many models cannot use TLS to a custom Stratum server. |
| Stock Whatsminer | TLS works; an M50 accepts the self-signed certificate. Other models and firmware versions may not; then use a trusted certificate, or TCP on a network you trust. |
| VNish | Check TLS on your version. Dev-fee connections the firmware opens itself bypass the proxy. |
| WMOC | Check TCP and TLS on your version. |

## TLS certificate

If neither `/data/certs/fullchain.pem` nor `/data/certs/privkey.pem` exists, the first start creates a self-signed certificate, valid for 10 years, for the names in `TLS_SELF_SIGNED_HOSTS`. It is reused after restarts; changing the variable does not reissue an existing certificate.

To use a trusted certificate, mount both files into the container, readable by UID 65532, and set `TLS_CERT_FILE` and `TLS_KEY_FILE` in `docker-compose.yml`. A changed pair is picked up without a restart (the files are checked at most once a minute).

Miners must use TLS 1.2 or newer by default. Lower `tls_min_version` only for old firmware.

## Environment variables

These configure the process and take effect after a container restart:

| Variable | Default | Purpose |
|---|---|---|
| `STRATUM_TCP_ADDR` | `:13333` | Stratum TCP listen address; `off` disables it. |
| `STRATUM_TLS_ADDR` | `:14443` | Stratum TLS listen address; `off` disables it. |
| `ADMIN_ADDR` | `:18080` | Admin UI and API listen address. |
| `ADMIN_USERNAME` | `admin` | Admin user name. |
| `ADMIN_PASSWORD` | required | Admin password. |
| `API_TOKEN` | empty | Bearer token for API clients; empty disables token login. |
| `DATA_DIR` | `/data` | Directory for `state.json`, history and certificates. |
| `TLS_CERT_FILE` | `/data/certs/fullchain.pem` | Certificate of the TLS listener. |
| `TLS_KEY_FILE` | `/data/certs/privkey.pem` | Private key of the TLS listener. |
| `TLS_SELF_SIGNED_HOSTS` | container host name | Comma-separated names for the self-signed certificate. |
| `PUBLIC_HOST` | empty | Host shown in the connection hints for miners. |
| `PUBLIC_TCP_PORT` | listener port | TCP port shown in the hints. |
| `PUBLIC_TLS_PORT` | listener port | TLS port shown in the hints. |
| `LOG_FORMAT` | `json` | Log format: `json` or `text`. |
| `TELEGRAM_API_URL` | `https://api.telegram.org` | Bot API server, for a local one or a mirror where Telegram is blocked. `HTTPS_PROXY` works too. |

`docker-compose.yml` fixes the listen addresses inside the container; set the published ports with `PUBLIC_TCP_PORT`, `PUBLIC_TLS_PORT` and `ADMIN_PORT` in `.env`.

## Runtime settings

Everything else is set in Settings in the admin UI (or with `PUT /api/settings`), stored in `state.json` and applied without a restart. If any value in a change is invalid, nothing is changed.

The screen shows the everyday groups open and the technical ones (timeouts, limits, TLS, history, logging, server) under Advanced, collapsed; the browser remembers what you opened. A group header sums up its state ("10 min of every 30 min", "Advise, every 1 d") and counts the values that differ from the defaults and the unsaved ones. The search box finds a setting by name, key or description.

| Setting | Default | Range | Meaning |
|---|---|---|---|
| `test_worker` | `proxytest` | 1–32 characters | Worker name used by pool checks. |
| `switch_drain` | 10 s | 0 s – 5 min | Time over which a switch, failback or "reconnect all" spreads the disconnects. |
| `failback_delay` | 2 min | 30 s – 1 h | How long the active pool must stay up before sessions return to it. |
| `pool_probe_interval` | 30 s | 10 s – 10 min | Health check period of every pool address: subscribe, authorize and a job within 10 seconds. |
| `profit_switch` | off | off, advise, auto | Profit switching mode. |
| `profit_interval` | 24 h | 1 h – 7 days | How often the coins are compared. |
| `profit_margin` | 5 % | 0–50 % | How much more another coin must earn before switching to it. |
| `profit_return_margin` | 1 % | 0–50 % | With a main pool: below this advantage the farm returns to it; at most `profit_margin`. |
| `timed_switch` | off | off, on | Timed switching. |
| `timed_period` | 30 min | 10 min – 24 h | How often the farm moves to the timer pool. |
| `timed_duration` | 10 min | 1 min – 12 h | How long it stays there in every period; shorter than `timed_period`. |
| `hunt_switch` | off | off, on | Block hunting on eCash. |
| `offline_after` | 1 min | 30 s – 1 h | An ASIC that sends no shares for this long is offline. |
| `telegram_chats` | empty | chat ids, comma-separated | Chats that get the alerts and may use the bot commands. |
| `telegram_language` | en | en, ru, fa | Language of the alerts and the bot answers. |
| `device_control` | off | off, on | Only customers and approved devices may mine. |
| `device_auth_timeout` | 30 s | 5 s – 5 min | A connection without a login that device control allowed is closed after this. |
| `device_pending_limit` | 10 | 1–100 | Devices of one customer that may wait for approval at once. |
| `operator_share` | off | off, on | The operator share switch. |
| `operator_share_kind` | percent | percent, minutes_per_day | Whether the amount is a percent of the time or minutes per day. |
| `operator_share_amount` | 2 | 0–144 | The proxy-wide size, in that kind. Customers and devices can have their own. |
| `operator_share_max_percent` | 10 % | 1–50 % | A cap on every size. |
| `operator_share_cycle` | 15 min | 1 min – 1 h | The cycle the operator's window sits in. |
| `tls_handshake_timeout` | 10 s | 1–60 s | Time a miner has to finish the TLS handshake. |
| `first_message_timeout` | 15 s | 5 s – 2 min | Time a new connection has to send its first Stratum message. |
| `upstream_dial_timeout` | 5 s | 1–30 s | Connect and TLS handshake timeout of one pool address. |
| `upstream_connect_budget` | 15 s | 5 s – 2 min | Total time a session may spend trying addresses and pools; at least `upstream_dial_timeout`. |
| `miner_idle_timeout` | 10 min | 1 min – 1 h | A miner that sends nothing for this long is disconnected. |
| `upstream_idle_timeout` | 15 min | 1 min – 1 h | If the pool sends nothing for this long, the session is closed and the miner reconnects. |
| `upstream_job_timeout` | 1 min | 10 s – 1 h | After authorization, maximum wait for a usable mining.notify. Other messages do not extend it. Must not exceed `upstream_idle_timeout`. Applies immediately. |
| `max_connections` | 5000 | 10 – 100000 | Miner connections in total. |
| `max_pending_connections` | 500 | 10 – 10000 | Connections that have not sent their first message yet; at most `max_connections`. |
| `max_conn_per_ip` | 0 (off) | 0 – 100000 | Connections from one IP address. |
| `max_line_bytes` | 64 KiB | 4–1024 KiB | Longest Stratum message from a miner or a pool; a longer one closes the session. |
| `tls_min_version` | 1.2 | 1.0, 1.1, 1.2, 1.3 | Oldest TLS version accepted from miners. |
| `history_detail_retention` | 7 days | 1–90 days | Per-minute totals and 10-minute points per miner. |
| `history_retention` | 365 days | 7 days – 10 years | Hourly totals; at least `history_detail_retention`. |
| `history_miner_retention` | 90 days | 7 days – 10 years | Hourly points per miner; at least `history_detail_retention`. |
| `log_level` | info | debug, info, warn, error | Log detail. Passwords are never logged. |

Pool health means the address accepts the configured test worker and provides a usable `mining.notify`, not just a TCP connection. Two failed checks mark an address DOWN; two successful checks confirm recovery. Checks run in the background, from the Test button and before a switch. The test worker may appear as inactive in the pool dashboard.

A rejected miner authorization is forwarded to the ASIC, its session is closed, and a full check of that address is requested. Requests are combined and background checks of an address start at most once every 10 seconds. A single worker rejection does not exclude the address without failed checks. Saved-pool manual tests update health; testing unsaved editor settings does not.

If an authorized session receives no usable jobs for `upstream_job_timeout`, the address is immediately excluded, even if it still sends difficulty changes or other messages. Open sessions on failed addresses reconnect over `switch_drain`, first through another address of the same pool, then through the ordered fallback pools. This also applies to failed fallback pools. The selected active pool and fallback order stay saved; return to the active pool waits for full checks and `failback_delay`.

## Charts and history

Every minute the proxy records what changed: hashrate (from accepted share difficulty), accepted and rejected shares, and connected miners, in total and per pool. For each miner (ASIC login) it records hashrate, shares and time online in 10-minute points. Finished hours are rolled up into hourly points.

| File | Contents | Kept for |
|---|---|---|
| `/data/history/minutes/YYYY-MM-DD.jsonl` | per-minute totals | `history_detail_retention` |
| `/data/history/hours/YYYY-MM.jsonl` | hourly totals | `history_retention` |
| `/data/history/workers-10m/YYYY-MM-DD.jsonl` | 10-minute points per miner | `history_detail_retention` |
| `/data/history/workers-hours/YYYY-MM.jsonl` | hourly points per miner | `history_miner_retention` |

Ranges of up to three days are drawn from the detailed points, longer ones from the hourly points. The totals take about 0.3–0.5 MB a day per minute and 10 KB a day hourly; each miner adds about 8 KB a day in 10-minute points and 1.3 KB a day hourly. Expired files are deleted whole. The files are JSON lines, one point per line, and are backed up with the volume. There is no database.

A crash loses at most the unfinished interval: one minute of totals and ten minutes of per-miner data. A normal stop writes both.

On the Miners page, click a miner to see its charts. The same page lists miners seen in the last 24 hours that are not connected now.

## Profit switching

The proxy can compare the SHA-256 coins of your pools at a set interval and pick the most profitable one, for example BTC or BCH.

1. Add pools for different coins (the Coin field: BTC, BCH, BSV, XEC, DGB or FB) and tick "Take part in profit switching" in each of them.
2. In Settings, set profit switching to Advise, which shows the result on the Dashboard with a button to switch, or to Auto, which switches by itself.
3. Adjust the interval (24 hours by default) and the margin (5 % by default: another coin must earn at least that much more than the current one).

Revenue is estimated from 24-hour averages of network difficulty, block reward (fees included) and price from [WhatToMine](https://whattomine.com): one request per check, no API key. WhatToMine does not list BSV; its difficulty and price come from [WhatsOnChain](https://whatsonchain.com), and its block reward is the subsidy (BSV fees are negligible). Pool fees and payout schemes are not taken into account. When there is no data, or it is stale, nothing changes.

eCash (XEC) needs a correction. Its nodes enforce Real Time Targeting: besides the target in the block header, a block has to meet a real-time target that depends on how long ago the last blocks arrived. A block 30 seconds after the previous one has to be about 800 times harder, after a minute 25 times, after two minutes it no longer matters. Nobody finds blocks in those minutes, and the difficulty algorithm lowers the header difficulty so that blocks still come every 10 minutes. A difficulty-based estimate therefore overstates what miners get; simulating the node's rule gives 0.757 of it, and XEC revenue and odds are multiplied by that. The formula is the one in Bitcoin ABC, `src/policy/block/rtt.cpp`.

A profit switch works like a manual one: the target pool is checked, the change is saved and miners reconnect gradually. Its reason, `profit`, shows in the events and in the last switch on the Dashboard. If the active pool does not take part, it is left alone. Solo pools never take part. "Check now" only shows the decision and never switches. The time of the last scheduled check is stored in `/data/profit.json`, so a restart does not move the schedule.

On PPLNS pools frequent switching loses earnings. Use FPPS or PPS+ pools and an interval of a day or more, and start with Advise.

### Main pool

Without a main pool the farm stays on whichever coin it switched to until another one is the margin ahead. With one, chosen under Settings → Profit switching (any pool that takes part, for example BTC), the farm comes back to it:

- On the main pool: the farm leaves for a coin that earns at least `profit_margin` more (5 % by default).
- Away from it: once the current coin earns less than `profit_return_margin` more than the main pool's coin (1 % by default), the farm returns to the main pool and stays there until a coin is `profit_margin` ahead again. A coin that beats the current one by `profit_margin` still wins.

For example: BTC is the main pool, BSV gets 6 % ahead and the farm moves to it; when BSV's lead falls to 0.8 %, the next check brings the farm back to BTC. The gap between the two margins keeps it from bouncing on small changes. If the main pool is down at that moment, the farm stays where it is and the report says why. The returns show in the events as `profit_returned`.

## Timed switching

Timed switching sends the farm to another pool for part of every period and brings it back, for example to a solo pool for 10 minutes of every 30 minutes: a third of the hashrate plays the solo lottery.

1. Add the pool, for a solo pool usually with your payout address in the login template (`bc1q....{worker}`), and tick "Switch to this pool on the timer" in its editor. Only one pool can have it. Tick "Solo pool" too if it is one (see below).
2. In Settings, turn on timed switching and set the period (30 minutes by default) and the time on the timer pool (10 minutes by default).

Periods start at multiples of the period: with 30 minutes, at :00 and :30 (after "Start now", from the moment it was pressed). From the start of each, the farm spends the set time on the timer pool: the timer pool is checked and made active, and when the time is up the farm returns to the pool that was active before, with the fallback order from before. While the farm is on the timer pool, the pool it came from is the first fallback. The switches show in the events with the reason `timer`.

- If the timer pool fails its check, the farm stays where it is until the next period.
- If you switch pools by hand during that time, your choice stays and the timer does not switch back.
- A restart in the middle does not strand the farm: the way back is saved in `/data/timed.json`.
- Profit switching waits with its scheduled check until the farm is back, and compares coins for the pool it returns to.
- "Start now" in the timer panel on the Dashboard does not wait for the next period: the farm goes to the timer pool at once, and the schedule starts over from that moment. With 30-minute periods pressed at 12:17, the farm is there until 12:27 and goes again at 12:47. On the timer pool already, its time there starts over.

When the timer pool mines eCash, the timer follows the real-time target (see profit switching above). It does not go to the pool while a block would be more than 1.2 times harder than its header says, which is the first two minutes or so after a block. When a block arrives while the farm is there and the next one becomes more than twice as hard, the farm goes back to the other pool until that eases, and the time is made up later in the period. With less than a minute of time left, it just stays. When a period ends while the farm is on the timer pool, it stays there for the new period instead of leaving and coming back.

To know when eCash blocks arrive, the proxy keeps one quiet Stratum connection to an eCash pool (the timer pool if it mines eCash, else the first one) and watches its jobs: a job with a new previous-block hash means a block. It logs in as the test worker, which the pool may show as an idle worker. The times of the 17 blocks before are read once from Blockchair at start.

The Dashboard shows the network difficulty of every coin (latest and against the 24-hour average) and the farm's chances to find a block solo at its current hashrate: per hour, per day and the average time to a block, plus the chance per day with the timer. For eCash it also shows the real-time difficulty now and when the last block came, like solo pools do. The market data is cached for 10 minutes; the panel works with profit switching off.

Every switch reconnects all miners within `switch_drain`, so a 30-minute period costs four reconnects an hour. Switching away from a PPLNS pool also loses part of its reward window.

### Block hunting on eCash

Right after an eCash block the next one has to be hundreds of times harder (Real Time Targeting, see profit switching above); about two minutes later it is back to the difficulty in its header, which then stays until the next block. A solo miner on eCash wastes those first minutes after every block. Block hunting skips them: the farm goes to the XEC solo pool once a block is at most 1.2 times harder than its header, and returns to where it was as soon as a block is found, by the farm or anyone else. Waiting longer than those two minutes gains nothing: a block is as likely in any minute after that.

1. Add the eCash solo pool with coin XEC and tick "Solo pool".
2. In Settings, turn on block hunting.

- The blocks are seen through the connection to the eCash pool described above, and the farm leaves within a fraction of a second of a block. Without that connection hunting waits, and a hunt in progress ends.
- It runs along with the others. The farm is away for one reason at a time, and the timer comes first: a hunt in progress makes way for the timer's period, and hunting resumes after it. When the timer's pool is the same eCash pool, the farm stays there and only the reason changes. Profit switching picks the pool the farm returns to and waits with its scheduled check while the farm is away.
- A pool chosen by hand stands: hunting waits for the next eCash block before it moves the farm again. After a failed switch it waits 5 minutes.
- Every eCash block, about every 10 minutes, costs two reconnects of the farm. The switches show in the events with the reason `hunt`; the event log keeps 2000 entries, about two days of hunting.
- The Dashboard shows where hunting is now and its last 24 hours; so does the Telegram `/status`.

### Solo pools

"Solo pool" in the pool editor marks a pool that pays only for the blocks the farm finds. It is a mark on top of the role: a solo pool can be active, a fallback or the timer pool. It shows as Solo next to the role in the pool list, on the Dashboard when the farm mines there, in the solo odds next to its coin and in the Telegram `/status`. A solo pool never takes part in profit switching: comparing coins, it could move the whole farm to the lottery for good.

## Customers and device control

Device control is off by default: the proxy is open to every miner, as in the original. Turn on `device_control` in Settings (Devices) to let only approved devices mine.

1. In Devices, add a customer. Each customer has a short name (the slug), an optional pool account name, optional limits (devices and connections) and an optional end date. The customer's **connection key** is shown once, when the customer is made or the key is changed. It is not stored; only its hash is.
2. The customer points their miners at the proxy and logs in as `<slug>.<worker>` with the connection key as the password. The part after the first dot is the device's name.
3. A new device is recorded as **pending** and does not mine. You approve it in Devices and choose how long it works: a week, a month, three months, six months, a year, no end, or a date of your own. Reject, suspend, resume, block, unblock and revoke are there too. The end date can be changed later.
4. Everything is checked on the server on every login and when a device's state changes: a device that is not approved, has expired, or belongs to a suspended or blocked customer has its connections closed. Blocking or suspending one device never disconnects the customer's other approved devices.

A wrong key is not a pending device: logins with a wrong key are counted per address and lock that address for a while after too many. A device with the right key that is merely pending or over a limit does not count as a failure, so customers behind one NAT address do not lock each other out.

Customers have no access to the panel. Everything about customers and devices is done by the administrator.

Settings: `device_control` (off), `device_auth_timeout` (30 s), `device_pending_limit` (10 devices waiting per customer).

## Operator share (optional)

The operator share lets the farm host take a small, fixed part of a customer's **mining time** as work for the host's own worker. It moves no money: the pool pays whoever a share is credited to, and Bitlite only records how much work went where. It never redirects a customer's mining without the customer's knowledge and agreement.

It is off by default, and a share is routed to the operator only when **all** of these hold, checked on the server for every share:

- `operator_share` is on in Settings (Operator share);
- the pool is marked "credits a second worker" in the pool editor, with the operator's login on that pool;
- you recorded the customer's consent in Devices (who recorded it and what terms are kept in the audit log; withdrawing it stops the share at once);
- the device is approved and the rule gives it a share above zero.

How it works: during short fixed windows of every cycle (`operator_share_cycle`, 15 minutes by default) the proxy puts the operator's worker in the worker field of `mining.submit` instead of the customer's. The miner never stops and never reconnects. The size is a percent of the time, or minutes per day (one hour a day is about 4.17 %), and is capped by `operator_share_max_percent` (10 % by default) whatever a rule says.

Sizes can be set for the whole proxy (Settings), for a customer, and for one device in Devices. The device's own size comes first, then the customer's, then the setting; a size of 0 leaves that customer or device out. Setting a size needs the `billing.manage` permission and is written to the audit log. A size is never a consent.

The Operator share page shows, for any period and customer, the time given to the operator, the number of windows, and accepted and rejected shares and difficulty on both sides. It shows no income and no payments, because Bitlite does not see any. The table can be downloaded as CSV.

If a pool refuses the operator's worker, or rejects its shares, the share switches itself off on that pool for a while and the page says why. The customer's mining carries on as before.

**Important:** it only works with a pool that accepts a second worker on the connection the miner opened and credits shares to it. The pools' documentation does not say whether ViaBTC does, and this has **not** been verified. Do not rely on it, and do not tell customers it works, until you have checked it yourself on a test account (below).

### Checking a pool for the operator share

In the pool editor, with the operator share ticked and the operator's login filled in, **Check second worker** connects to each address, logs in as the test worker, then sends the operator's `mining.authorize` on the same connection, exactly as a session does. It shows each step: connect, subscribe, authorize, job, second authorize, and whether the connection stays open. The verdict is one of: second authorize accepted, refused (with the pool's reason), no answer, connection closed, not reached.

It sends no share, so it **cannot** show that the pool credits shares to the second worker; a pool may accept the second login and still credit everything to the first. To be sure:

1. Use a test account on that pool, never a customer's.
2. Turn the operator share on, record consent for a test device and mine a little through the proxy.
3. Open the pool's own dashboard: the operator's worker must show accepted shares of its own.

## Users, roles and the audit log

The administrator from `ADMIN_USERNAME` and `ADMIN_PASSWORD` can make other accounts (Users). A role is a set of permissions; `superadmin` has all of them and `operator` can watch the farm and handle devices but cannot change settings or accounts. Permissions can be granted or revoked per account:

| Permission | Allows |
|---|---|
| `stats.view`, `miners.view`, `events.view` | Dashboard, charts, miners, events |
| `miners.control` | Reconnect all miners |
| `settings.view`, `settings.edit` | See, change pools, settings and the bot |
| `devices.view`, `devices.manage` | See, approve, block and limit customers and devices |
| `billing.view`, `billing.manage` | Operator share account; consent and share sizes |
| `audit.view` | The audit log |
| `users.manage` | Accounts, roles and passwords |

The audit log records logins, denied requests, and changes to users, customers, devices, pools and settings, with the account, address, target, result and details. Passwords, keys and tokens are never recorded. The Audit log page filters by period, group of actions, result and text, pages through older entries and downloads a CSV.

## Monitoring and alerts

A working ASIC sends shares every few seconds. Every 5 seconds the proxy looks at the connected ASICs, and one that has sent no share for `offline_after` (1 minute by default) is offline, whatever the reason: powered off, hung with its connection still open, cut off from the network or pointed at another pool. The connection alone says little: a powered-off ASIC leaves it open until `miner_idle_timeout`.

- An ASIC that shares rarely (low hashrate or a high share difficulty) gets 20 of its usual intervals between shares instead, so a slow miner is not reported between two shares.
- For 5 minutes after the proxy starts nobody is reported: the ASICs are reconnecting.
- ASICs are told apart by the worker name. ASICs that share a name are watched as one, and one of them stopping goes unnoticed while the others send shares, so give every ASIC its own name.
- An ASIC is watched from its first share and dropped from the list after 7 days offline.
- The state is kept in `/data/monitor.json`: after a restart an ASIC that was already reported is not reported again.

The changes go to the events. To get them in Telegram, in Settings under Monitoring and alerts:

1. Create a bot with [@BotFather](https://t.me/BotFather) and paste its token into "Bot token". The proxy asks Telegram whether the token works before saving it, and the bot starts at once, without a restart. The token is kept in `state.json` and the API never returns it.
2. Send `/start` to the bot from your chat; for a group, add the bot to it first. A chat that is not allowed yet gets its id in the answer.
3. Put the id into `telegram_chats` (several ids are separated by commas) and choose the language. "Send a test message" checks the setup.

The bot asks Telegram for new messages itself (long polling), so the proxy needs no public address or webhook. It answers only the chats in `telegram_chats`:

| Command | Answer |
|---|---|
| `/status` | Hashrate, how many ASICs are mining, the pool and its state, the timer, shares since start, the ASICs without shares |
| `/miners` | Every ASIC with its hashrate, or how long it has been silent |
| `/help` | The commands |

The alerts are kept few. A message goes out at most once a minute and holds everything since the previous one. The first comes 10 seconds after a change, so a rack that loses power is one message. An ASIC that comes back before the message is sent is left out of it. An ASIC that has already dropped out and come back within the hour is reported at most every 15 minutes, with the state it is in then. The events keep every change.

## API

The admin UI uses a JSON API under `/api/`. Scripts authenticate with `Authorization: Bearer <API_TOKEN>`. Requests that change something must be sent with `Content-Type: application/json`.

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/health` | Liveness check, no login needed |
| GET | `/api/status` | Mode, active pool, miners, shares, hashrate |
| GET | `/api/miners` | Connected miners |
| POST | `/api/miners/reconnect` | Reconnect all miners over `switch_drain` |
| GET | `/api/events?limit=100&level=warn` | Recent events; `level` (info, warn, error) leaves out the lower ones |
| GET | `/api/pools` | Pools; passwords are never returned |
| POST | `/api/pools` | Add a pool |
| PUT | `/api/pools/{id}?reconnect=true` | Change a pool; `reconnect` applies it to connected miners now |
| DELETE | `/api/pools/{id}` | Delete a pool (not the active one) |
| POST | `/api/pools/test` | Check pool settings before saving |
| POST | `/api/pools/{id}/test` | Check a saved pool |
| POST | `/api/pools/{id}/activate?force=true` | Make a pool active; `force` skips the check |
| PUT | `/api/fallback` | Fallback order: `{"pools": ["pool_b", "pool_c"]}` |
| GET, PUT | `/api/settings` | Runtime settings; `null` resets a value to its default |
| PUT | `/api/telegram/token` | Set the Telegram bot token after Telegram accepts it: `{"token": "..."}`; `""` removes it |
| POST | `/api/telegram/test` | Send a test message to the Telegram chats |
| GET | `/api/history?from=&to=&points=` | Totals and per-pool history; times in unix seconds |
| GET | `/api/history/worker?name=&from=&to=&points=` | One miner's history and a summary of the range |
| GET | `/api/history/workers?from=&to=` | Every miner seen in the range |
| GET | `/api/profit` | Profit switching status and the last report |
| POST | `/api/profit/check` | Compare the coins now; never switches |
| PUT | `/api/profit/home` | The main pool of profit switching: `{"pool": "btc"}`; `""` for none |
| GET | `/api/timed` | Timed switching: target pool, time still due, next period, eCash wait; block hunting in `hunt` |
| POST | `/api/timed/start` | Go to the timer pool now and restart the schedule from this moment |
| GET, POST | `/api/users`, `/api/users/{id}/password` | Accounts and passwords; `GET /api/roles` lists roles and permissions |
| GET | `/api/audit?from=&to=&action=&q=&limit=` | The audit log; `format=csv` downloads it |
| GET, POST | `/api/customers` | Customers; the connection key is returned once, on creation |
| POST | `/api/customers/{id}/status`, `/key`, `/fee-consent` | Customer state, a new connection key, record or withdraw consent |
| PUT | `/api/customers/{id}/operator-share`, `/api/devices/{id}/operator-share` | Size of the operator share: `{"kind":"percent","amount":3}` or `{"inherit":true}` |
| GET | `/api/devices` | Devices; `POST /api/devices/{id}/action` approves, suspends, blocks ... with an optional `expires_at` |
| POST | `/api/pools/test-operator` | Check a pool for a second worker on one connection |
| GET | `/api/operator-share/usage?from=&to=&customer=` | The operator share account per device and day |
| GET | `/api/network` | Difficulty, block reward, price and efficiency of the coins; eCash real-time target |

Errors have the form `{"error": "validation", "key": "...", "params": {...}, "message": "..."}` with status 400, 404, 409 or 422 (pool check failed). `message` is English text for scripts; the UI translates `key` with `params`.

## Network and firewall

Allow the Stratum ports only from your farms' IP addresses where you can. Ports published by Docker bypass UFW rules, so use your provider's firewall or the `DOCKER-USER` iptables chain.

`max_conn_per_ip` is off by default because a farm usually connects from a single NAT address. If you turn it on, leave room above the number of miners behind one address.

The proxy makes outgoing HTTPS requests for market data to whattomine.com and api.whatsonchain.com: with profit switching on, once per interval, and while the Dashboard is open, at most once every 10 minutes. With an eCash pool configured, it also reads recent block times from api.blockchair.com once at start and keeps one Stratum connection to that pool. With a Telegram bot set up, it keeps a long-polling HTTPS request open to api.telegram.org.

## Operations

```bash
docker compose ps
docker compose exec proxy /app/proxy healthcheck
docker compose logs --tail=200 proxy
```

To update, back up the volume first, then:

```bash
git pull
docker compose up -d --build
```

`state.json` stores pool passwords and the Telegram bot token in plain text: protect the backups and access to the Docker host.

On stop (SIGTERM) the proxy stops accepting connections, closes the miner sessions and exits within 10 seconds. Miners reconnect by themselves.

## Development

You need Go 1.24 or newer and Node.js 24 or newer. Node only builds the admin UI; the Docker build runs it in a separate stage.

```bash
# Admin UI: dependencies, type check, lint, tests, build into web/dist
cd web
npm ci
npm run check
npm run build
cd ..

# The proxy, with the built UI embedded
go vet ./...
go test ./...
ADMIN_PASSWORD=dev-password DATA_DIR=./data LOG_FORMAT=text go run ./cmd/proxy
```

It listens on `:13333`, `:14443` and `:18080`; `STRATUM_TLS_ADDR=off` turns the TLS listener off. Without `npm run build` the proxy still runs and shows how to build the UI in its place.

For UI work, run `npm run dev` in `web/`: Vite reloads the page on changes and forwards `/api` to a proxy running on `127.0.0.1:18080`.

| Path | Contents |
|---|---|
| `cmd/proxy` | Entry point, wiring, graceful shutdown |
| `internal/session`, `internal/stratum` | Miner-to-pool relay, Stratum parsing and rewriting |
| `internal/listener` | TCP and TLS listeners, connection limits |
| `internal/pool` | Pools, address checks, failover and failback, drain |
| `internal/stats`, `internal/history` | Counters in memory, history on disk |
| `internal/profit` | Coin revenue data and profit switching |
| `internal/rtt` | eCash Real Time Targeting: the formula and the connection that sees blocks arrive |
| `internal/timed` | Timed switching and block hunting: away to another pool and back |
| `internal/monitor`, `internal/telegram` | ASICs that stop sending shares; the Telegram bot and its alerts |
| `internal/admin`, `internal/apierr` | REST API, login and brute-force protection, error keys |
| `internal/settings`, `internal/state` | Runtime settings, `state.json` |
| `internal/tlsutil`, `internal/atomicfile` | TLS certificates, atomic file writes |
| `web/src` | Admin UI in React and TypeScript; translations in `web/src/i18n/locales` |

The server does not translate text. API errors carry a key and parameters, and the UI translates them; `web/src/i18n/locales.test.ts` checks that every key used in the Go code and every setting is translated into all three languages (English, Russian, Persian). The Telegram bot is the exception: its messages are made on the server, from the texts in `internal/telegram/texts.go`.

## Security notes

- Keep the admin port on localhost and reach it through an SSH tunnel or a VPN; if it must be reachable from the internet, put it behind an HTTPS reverse proxy. The UI itself is plain HTTP.
- Use a long, unique `ADMIN_PASSWORD`, one account per person, and keep `API_TOKEN` and the Telegram bot token in environment variables or the admin UI, never in the repository.
- Keep ASICs on a separate network that may reach only the proxy, and change the default password of their own web panel.
- Open the Stratum ports only to the addresses your miners connect from. Prefer the TLS port where the firmware supports it; otherwise use a VPN between the farm and the proxy.
- Turn on `device_control` when other people's miners connect, so that only approved devices mine.
- `state.json` and `devices.json` hold pool passwords, the bot token and customer records in plain text, and the connection keys are stored as hashes: protect the Docker host and the backups.

## Limitations

- Stratum V1 only (line-delimited JSON-RPC), no Stratum V2.
- One pool connection per miner; connections are not aggregated.
- The counters in the miners table start from zero after a restart; the history for the charts is kept on disk.
- Profit switching ignores pool fees, payout schemes and payout delays.
- No built-in ACME / Let's Encrypt.
- The operator share needs a pool that credits a second worker on one connection. It is unverified for ViaBTC: test it on your own account first.
- There is no billing engine yet: Bitlite records work, not money. It issues no invoices and confirms no payments.
- Customers cannot sign in; everything is managed by administrators.
- The Jalali calendar is for display only, in Persian; stored times and exports are UTC.
- Firmware, certificates and pools differ: try a few miners before moving the whole farm, and compare the reject rate with a direct pool connection.

## License

GPL-3.0. See [LICENSE](LICENSE).
