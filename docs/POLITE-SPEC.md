# Polite mode (POLITE-SPEC)

Polite mode lets Downex slow its own downloads when they make the line laggy (calls, games,
streaming) and speed them up again when the line is quiet. It is receive-side and
delay-based, needs no admin rights, sends no telemetry and is **off by default** (the owner's
speed-first rule). Written from public standards (RFC 6817 §2.4, base delay history) and
Downex's own design.

**Glossary.** *Polite mode* is this feature. It is not ENGINE-SPEC §10 "Errors & politeness",
which is the engine's per-host back-off after 429/503/421 and refused connections; that
document calls the latter the *host penalty*.

Code: `internal/polite` (controller, monitor, probers), `internal/transport/peers.go`
(`PeerTable`), `internal/daemon/polite.go` and `settings_polite.go`, the clients listed in §8.

Part A only. Part B (a per-queue `ignore_polite` flag with a store migration) is not built.

## 1. Purpose and scope

- Measures the line with small probes to servers Downex already downloads from, and holds every
  download together under one cap R when the extra delay over the line's base passes a target
  (default **30 ms**).
- Never raises a limit, never stops a download (R ≥ 64 KiB/s), and fails open: when the line
  cannot be measured, downloads run at full speed.
- Not passive own-connection RTT in v1: Windows `SIO_TCP_INFO` and macOS `TCP_CONNECTION_INFO`
  report the sender-side smoothed RTT, which hardly moves on a receive-only flow (Windows clients
  negotiate no TCP timestamps by default); `GetPerTcpConnectionEStats` needs admin to enable its
  collection; probing the gateway misses the provider's downlink queue and would trigger macOS's
  Local Network prompt.

## 2. Anchors and probes

- **Rule 1.** **Peers.** `transport.PeerTable` records, per remote IP:port, the live connections every slot
   of the transport factory dials (`router.dialContext` wraps each successful connection;
   `Close` releases it once): the origin of a direct connection, else the proxy's address (SOCKS,
   CONNECT tunnel, forwarding proxy). Per address it keeps, in memory only, the host names of the
   targets reached through it, used only to check host penalties (rule 4); no URLs. A connection
   to an address without other live connections calls the table's `OnNew` hook (the daemon wakes
   the monitor, which probes it at once, before its download loads the line: rule 7).
- **Rule 2.** **Eligible anchor.** A peer address (IPv4-mapped addresses judged as IPv4) qualifies only if it
   is global unicast: never loopback, private (RFC 1918, ULA), link-local, multicast,
   unspecified, 100.64.0.0/10 or 198.18.0.0/15 (fake-IP TUN proxies). Downex never probes the
   LAN, so macOS 15's Local Network prompt never appears. Builds with the `e2etest` tag may allow
   loopback (`DOWNEX_E2E_POLITE_LOOPBACK=1`).
- **Rule 3.** **Selection.** Peers are merged per IP address (connections summed; TCP probes go to the port
   with the most connections). At most `MAX_ANCHORS` = 3 are selected on every refresh: most live
   connections first, then the earliest first-seen. An anchor that drops out keeps its history for
   10 min.
- **Rule 4.** **Probes.**
   - One probe at most every `PROBE_SPACING` = 250 ms, round robin over the selected anchors;
     at most one in flight per anchor and 3 in total (≤ 4 probes/s).
   - Timeout 1 s. A timeout or an unreachable answer gives no sample.
   - ICMP echo first, 32-byte zero payload: `IcmpSendEcho2`/`Icmp6SendEcho2` on Windows (no admin,
     no raw socket), unprivileged datagram ICMP sockets (`golang.org/x/net/icmp`, `udp4`/`udp6`)
     on macOS and Linux; a system that refuses them (EACCES/EPERM, Linux outside
     `ping_group_range`) uses TCP probes for every anchor.
   - After 3 consecutive ICMP probes without an answer from an anchor that never answered one, the
     anchor switches to **TCP connect probes**: to the peer's own port, `SetLinger(0)` and close at
     once (no request is ever sent, no TIME_WAIT), a refused connection (RST) counts as a sample.
     At most **one TCP probe per anchor every 2 s** (well under one per second per server), and
     **none to an anchor through which a host with an active host penalty is reached**
     (ENGINE-SPEC §10). Probes are made outside the transport, so they never count against the
     engine's connection accounting nor appear in the peer table. ICMP is tried again after 5 min.
   - An anchor that did answer ICMP but then loses 3 in a row while d is unknown where it is
     needed (NO_SIGNAL, or a loaded line outside a settle, rule 10) becomes **lossy** for 5 min:
     its echoes go on, and a TCP probe (same limits) takes the place of an echo whenever one is
     due. *Why:* a server that rate-limits ICMP (17.253.73.207, Apple's devstreaming-cdn.apple.com,
     answered about 1 echo in 5 on the owner's line, idle or loaded) had answered once, so it never
     moved to TCP; its rare samples seldom put 2 in rule 8's window, and an HLS download from it at
     full speed went WATCHING → NO_SIGNAL 12 s in. Losses alone never add TCP probes (a congested
     line drops echoes too, and d known from any anchor stops it), and the echoes keep their pace,
     so a wrongly lossy anchor costs one connection attempt every 2 s, not the reaction time.
   - Probes never go through proxies: the anchor already is the proxy when downloads use one.
- **Rule 5.** **RTT.** Measured around the call with a monotonic high-resolution clock (Windows: the
   performance counter, since Go's monotonic clock there can advance in 15.6 ms steps). The
   controller timestamps each sample with its own clock (`sentAt`).

## 3. Base and delay

- **Rule 6.** **Base.** Per anchor, the minimum RTT over 10 one-minute buckets of `sentAt`, the current one
   included (RFC 6817 §2.4). A path change resolves within 10 min.
- **Rule 7.** **Trust.** An anchor feeds d only once trusted:
   - (a) its first sample was sent while the line was quiet: Downex received under
     `QUIET` = 131,072 B/s over the preceding 2 s **and** since the last tick (a download that has
     just started fills the line long before its 2 s average shows it), or the trusted anchors
     put d at most TARGET/2 at the last tick (the cap holds the line quiet);
   - (b) it answered a probe sent at least 1 s into a calibration.
- **Rule 8.** **Decision window.** From max(now − `FILTER` (1.5 s), settleUntil, the last tick gap) to now,
   where settleUntil is engage + 3 s, decrease + 500 ms, or a tick gap + 3 s; a TCP probe's sample
   uses `TCP_FILTER` = 2 × 2 s + the 1 s probe timeout = **5 s** in place of `FILTER`. For each
   trusted anchor with samples in it, fa = max(0, its least RTT in the window − its base); d is the
   lower median of the fa values (index (n−1)/2 ascending) and is known only with at least 2
   samples in the window in total. (The design proposed a 3 s window; the §9 simulation showed it
   lets the queue reach 150–200 ms before the first cut.)
   - *Why TCP samples have their own window:* rule 4 allows one TCP probe per anchor every 2 s, so
     a 1.5 s window never held 2 samples of one anchor. A download from a server that does not
     answer ICMP (edgedl.me.gvt1.com, the Android Studio installer's host, on the owner's line)
     then never had a d: WATCHING → MEASURING (rule 7b trusted the anchor) → WATCHING → NO_SIGNAL
     10 s later, at full speed for the rest of the download, while servers that answer ICMP
     worked. `TCP_FILTER` holds an anchor's latest two TCP samples (two probe spacings plus the
     round trip), so d keeps resting on at least 2 samples. The cost: over TCP probes d reacts in
     about 4–5 s rather than 1.5 s, and each decrease waits that long for 2 fresh samples.

## 4. Controller

- **Rule 9.** **Parameters.** Ticks every `TICK` = 500 ms. X0.5, X2, X10 are Downex's receive rate over the
   last 1, 4 and 20 ticks (byte meter, rule 16). C, the line's capacity, is the highest X10 seen
   while WATCHING in the last 24 h.

   | Name | Value | Use |
   |---|---|---|
   | TARGET | `polite_target_ms`, default 30 ms (10–200) | extra delay kept under |
   | FLOOR | 65,536 B/s | lowest cap |
   | QUIET | 131,072 B/s | trust and calibration |
   | FILTER / TCP_FILTER | 1.5 s / 5 s | decision window (ICMP / TCP probe samples) |
   | ENGAGE_SETTLE / DECREASE_SETTLE | 3 s / 500 ms | wait for fresh samples |
   | CUT_GAIN | 4 | decrease factor max(0.5, 1 − (d−T)/(4T)) |
   | INCREASE | max(FLOOR/4, R/16) per tick | far from the last overload |
   | PROBE | max(FLOOR/16, R/1024) per tick | within [0.9, 1.05] × rHigh |
   | BINDING | 0.95 | the cap binds when X0.5 ≥ 0.95·R |
   | RELEASE_HOLD | 10 s, doubling to 5 min | release backoff |
   | STARVE | 15 min under 4·FLOOR | starvation guard |
   | NO_SIGNAL_AFTER / NO_TRUST_AFTER | 10 s / 5 s | fail-open, calibration trigger |
   | CALIBRATE | 1.5–4 s at FLOOR, spaced 60 s (10 min) | learn the base |

- **Rule 10.** **States.**
    - **OFF:** setting off. R = 0, no probes, no ticks.
    - **IDLE:** on, nothing downloading. R = 0, no probes, no ticks (a download start wakes it).
    - **WATCHING:** R = 0.
      - d > TARGET on 2 consecutive ticks and X2 ≥ 2·FLOOR → **SLOWED** with R = max(floor,
        0.75·X2), rHigh = X2, settleUntil = now + 3 s.
      - No trusted live anchor for 5 s while X2 ≥ QUIET → **MEASURING**, when allowed: some
        selected anchor answered within 5 s, and 60 s passed since the last calibration (10 min
        after one that moved no anchor's base by 5 ms or more, unless an untrusted selected anchor
        was not part of it).
      - d unknown for 10 s while X2 ≥ QUIET → **NO_SIGNAL**.
    - **SLOWED:**
      - *Decrease* when d > TARGET: unless the queue is already draining (d below the d of the
        previous cut of this series: the receive buffers lag the cap by seconds), R =
        max(floor, from · max(0.5, 1 − (d − T)/(4T))) with from = min(R, X2) (a cap above what
        Downex receives would cut nothing); settleUntil = now + 500 ms. The first cut of a series
        sets rHigh = from, the rate that overloaded the line.
      - *Increase* when d ≤ TARGET and the cap binds (X0.5 ≥ 0.95·R): R += step·(T − d)/T, step
        max(FLOOR/16, R/1024) within [0.9, 1.05]·rHigh, else max(FLOOR/4, R/16); passing 1.05·rHigh
        forgets rHigh. R ≤ Rmax = max(1.5·X10, 4·FLOOR); an increase never lowers R.
      - *Release* once the cap does not bind for releaseHold and d ≤ TARGET/2 → WATCHING, R = 0.
        The cap does not bind when X10 < 0.8·R, or when Downex receives steadily under it
        (X0.5 within 5 % of X10 and under 0.95·R): the increase never raises a cap Downex does
        not use, so a download that wants a little less than R (its server's speed; the
        engine's ramp-up stops under a cap) held R just above its rate and the mode SLOWED for
        good. releaseHold doubles (to 5 min) when SLOWED re-engages within 30 s of a
        release, and returns to 10 s after 5 min without one.
      - *d unknown* (outside a settle): R is held; after 10 s → **NO_SIGNAL**, R = 0 (fail-open);
        no trusted live anchor for 5 s → MEASURING as in WATCHING.
      - *Starvation:* R below 4·FLOOR continuously for 15 min → floor = max(FLOOR, C/10). (Not
        "at FLOOR": the base history needs a low sample every 10 min, which lifts R for a few
        ticks.) The floor returns to FLOOR when R ≥ 4·floor or the state leaves SLOWED.
    - **MEASURING (calibration):** R = FLOOR for at least 1.5 s and at most 4 s; it ends early once
      every selected anchor has 2 samples sent ≥ 1 s after its start and its RTT stopped falling
      (the latest did not set a new low by 5 ms or more). Those anchors are trusted (rule 7b); the
      previous state resumes with its R. Switched on while downloads run: a calibration as soon as
      X2 is known (2 s), when X2 ≥ QUIET and allowed.
    - **NO_SIGNAL:** R = 0, probing continues; → WATCHING once d is known; the calibration rule
      applies.
    - **UNAVAILABLE:** the daemon has no prober or peers (test daemons).
- **Rule 11.** **Apply and publish.** The cap is applied only on a change between 0 and > 0 or of at least
    2 %. A status is published on every state change, and while SLOWED when R moved by at least
    10 % and at least 1 s passed since the last publish. This throttles only when Watch events
    are sent: `Status`, the tray's activity and every event carry the monitor's current status,
    so the line delay they show is that of the latest tick.
- **Rule 20.** **Sleep.** A tick gap over 5 s drops the samples and keeps the bases; SLOWED keeps R;
    settleUntil = now + 3 s; the rates are measured afresh; a calibration ends.

## 5. Limits interplay

- **Rule 12.** **Chain:** global (limiter root) → **polite** (`chainLink`, own limit R) → queue → download.
    The cap in effect is the tightest; the engine sees it through `Limit()` (slice size, ramp-up),
    exactly as a user limit (no engine change: under a cap ramp-up stops at 85 % and reads are
    sliced; the stall timer does not fire while throttled). Polite mode never raises a limit; with
    a user limit that already keeps the line under the target it stays WATCHING.
- **Rule 13.** Schedules, queues, post-queue actions and keep-awake are unchanged; a slowed queue finishes
    later.
- **Rule 14.** File, media and site downloads are covered (every run waits on its `chainLink`); tool and
    update fetches and yt-dlp's resolution are not counted and not limited.
- **Rule 15.** **Off** applies R = 0 and publishes OFF before `UpdateSettings` returns, and stops probing.
- **Rule 16.** **Byte meter:** every run's link adds n to one shared counter at the top of `WaitN`, before
    the unlimited fast path. It counts bytes, never addresses.

## 6. Privacy

- **Rule 17.** Probes go only to addresses Downex already holds a live connection to (or its proxy), never
    to third parties, never to the LAN, never while idle.
- **Rule 18.** The log gets state changes, rate and delay only, never addresses. Nothing is stored beyond
    the settings; anchors, bases and R live in memory.

## 7. API and errors

- Settings **40** `polite_mode` (bool, default false) and **41** `polite_target_ms` (int32; 0 = 30;
  else 10–200), kept in `settings.json` as `"polite": {"enabled": false, "target_ms": 0}`; a
  missing object or field takes the defaults. (Fields 29–39 are left to other features.)
- `PoliteState` (UNSPECIFIED = an older daemon, OFF, IDLE, WATCHING, SLOWED, MEASURING,
  NO_SIGNAL, UNAVAILABLE) and `PoliteStatus {state, rate_limit, delay_ms, base_ms, since}`
  (rate_limit while SLOWED or MEASURING, else 0; delay_ms the latest known d, base_ms that
  anchor's base, −1 unknown).
- `Event.polite` = **20**: Watch without ids only; the first batch ends with one (after `tools`
  and `update`), later batches carry one after a change. `StatusResponse.polite` = **20**.
- **Rule 19.** **Validation:** `UpdateSettings` refuses `polite_target_ms` outside {0} ∪ [10, 200] with
    InvalidArgument "polite_target_ms must be 0 or between 10 and 200, got N"; an invalid file
    value is repaired to 0 and logged ("polite.target_ms: …; using the default 30 ms").

## 8. UI texts

- **Settings → Downloads:** switch row "Polite mode", "Slow down when downloads make calls, games
  or streaming lag; speed back up when the line is quiet." (after "Save downloads into their
  category's folder", spanning like it). The target is CLI/API only.
- **Status bar limit control:** SLOWED under the global limit (or none): "Polite 2.4 MB/s" in the
  warning ink; MEASURING: "Polite: measuring…" (warning ink); otherwise while on: LimitText +
  " · Polite"; off: unchanged. The setting off wins over a status that has not caught up.
  Tooltip "Global speed limit" + ". " + the line (one line: the Windows tooltip control shows a
  single line): IDLE/WATCHING/unknown "Polite mode is on: Downex slows down if its downloads make
  the line laggy."; SLOWED "Polite: slowed to keep the line smooth. Downloads are held to 2.4 MB/s
  while the line is busy."; MEASURING "Polite: measuring the line…"; NO_SIGNAL "Polite mode can't
  measure this connection, so downloads run at full speed."; UNAVAILABLE "Polite mode isn't
  available with this downexd.". Accessible name "Global speed limit: <text>. <line>".
- **Limit menu:** after "Custom…", a separated checkbox "Polite mode" (id `polite`, title "Slow
  down when downloads make calls, games or streaming lag"). Toasts "Polite mode on" / "Polite mode
  off" / "Could not change Polite mode: …".
- **Palette:** `polite`, Commands, gauge, "Turn on Polite mode" / "Turn off Polite mode", keywords
  "lag latency calls games streaming bufferbloat bandwidth", description off "Slow down when
  downloads make the line laggy", on "Now <state>" (SLOWED "Now slowed to 2.4 MB/s to keep the
  line smooth"); disabled like "Set global speed limit…".
- **Tray tooltip:** "Downex — 2 active, 2.4 MB/s · Polite: slowed" while SLOWED. No notifications.
- **CLI:** `downex settings --polite on|off --polite-target <ms>` (usage error "--polite-target must
  be between 10 and 200 (or 0 for the default), got N"), line `Polite mode: off | on (target +30
  ms)`, JSON `politeMode`/`politeTargetMs`; `downex daemon status` line `Polite mode:` "off", "on
  (nothing downloading)", "on, full speed (line delay +4 ms)", "slowed to 2.4 MB/s to keep the
  line smooth (line delay +42 ms)", "measuring the line", "on, but this connection can't be
  measured: full speed", "not available in this downexd"; none for an older daemon; JSON
  `polite {state, rateLimit, delayMs, baseMs}`.
- **Relay `status` answer:** adds `polite` ("off", "idle", "watching", "slowed", "measuring",
  "no_signal", "unavailable"; absent for an older daemon) and `politeRate` (bytes/s, only while
  slowed). **Extension popup:** the status chip's tooltip adds "Polite: slowed to keep the line
  smooth." while slowed; the chip's text is unchanged; read-only.

## 9. Tests

- `internal/polite`: the controller in virtual time (engage, decrease, drain hold, increase and
  slow probing, Rmax, release and backoff, release of a cap the download does not use, floor, starvation, NO_SIGNAL, calibration, trust,
  lower median, tick gap, ICMP→TCP, an anchor measured over TCP probes only (WATCHING with d
  known, never NO_SIGNAL; SLOWED when its RTT grows), the TCP sample window, a lossy anchor
  (TCP probes between its echoes while d is unknown, none while another anchor gives d), penalties, emit
  throttling); the anchors (eligibility,
  selection, base roll-over); `simline_test.go`, a fluid model of a 4.75 MB/s line with a 500 ms
  buffer, TCP-like flows (Downex and a competitor offer 1.05× the line uncapped), a lag τ of 1–3
  s, probe jitter and 2 % loss: (a) quiet line p95 queue delay ≤ TARGET + 10 ms and ≥ 85 % of the
  line, (b) a call's p95 ≤ TARGET + 15 ms, (c) a saturating competitor drives R to the floor and
  never below, (d) recovery within 30 s after it stops, (e) a +40 ms path change recovers within
  11 min, (f) all probes lost → NO_SIGNAL and R = 0 once d has been unknown 10 s, (g) a latecomer
  anchor does not stall Downex; the monitor under `testing/synctest` (≤ 4 probes/s, one in flight
  per anchor, nothing while off or idle, callbacks never under its lock, Stop leaves nothing);
  real-OS probes (Windows ICMP to 127.0.0.1 and ::1, unprivileged ICMP on macOS/Linux, TCP
  sending no byte).
- `internal/transport`: the peer table (direct, SOCKS5, forwarding and CONNECT proxies, 16
  concurrent slots). `internal/engine`: `HostPenalized`. `internal/daemon`: the chain, the meter,
  settings and file, Watch events, Status, Activity, UNAVAILABLE. CLI, relay, extension, window
  (state store, status bar, Settings switch, palette, app round-trip), tray tooltip.
- `internal/e2e/polite_test.go` (real downexd, e2etest build; skipped with `-short`): the probes
  answer after the milliseconds of the `DOWNEX_E2E_POLITE_RTT` file and loopback anchors are
  allowed (`DOWNEX_E2E_POLITE_LOOPBACK=1`). On a 2 MiB/s download: RTT 20 ms → "on, full speed
  (line delay +N ms)"; 150 ms → SLOWED, the cap under 256 KiB/s and the download really slower;
  20 ms → the cap back to 1 MiB/s or more (or removed); no file → NO_SIGNAL, full speed; off →
  OFF; no Polite mode log line names an address.

## 10. Unverified

1. The owner's real bufferbloated line (~38 Mbit/s): latency under load with Polite mode off and
   on, and the throughput cost on a quiet line. All parameters rest on the §9 simulation only.
2. Wi-Fi as the bottleneck.
3. Which CDNs answer ICMP (googlevideo, Cloudflare, Akamai, CloudFront/S3, GitHub releases) and
   how often the TCP fallback is used. Seen on the owner's line: 1.1.1.1, googlevideo and
   Cloudflare answer; edgedl.me.gvt1.com does not (TCP probes keep d known since rule 8's
   `TCP_FILTER`, checked with a real daemon: WATCHING with a delay for 40 s at 2 MB/s). Not
   checked: how fast SLOWED reacts over TCP probes on a really bufferbloated line. Apple's
   devstreaming-cdn.apple.com (17.253.73.207) answers about 1 echo in 5: before lossy anchors
   (rule 4) an HLS download from it at full speed went NO_SIGNAL 12 s in; with them one run
   went WATCHING → MEASURING → WATCHING → SLOWED with a delay for 25 s. One run each, so
   not checked: how much of that the TCP probes did rather than the calibration.
4. The real lag of the cap from autotuned receive buffers on Windows and macOS (EngageSettle,
   the drain hold, the calibration length).
5. Server send timeouts at the floor with many connections.
6. VPN/TUN apps, corporate and LAN proxies (expected NO_SIGNAL), IPv6-only paths.
7. macOS 13/14/15 unprivileged ICMP on Intel and Apple silicon, and no Local Network prompt;
   Linux `ping_group_range`.
8. Third-party firewalls prompting for ICMP.
9. Sleep and resume.
10. The extension's tooltip in a real browser; the window's status bar at 960–1100 px with
    "Unlimited · Polite".
11. Whether `SIO_TCP_INFO`/`TCP_CONNECTION_INFO` RTTs move under a receive-only load (a passive
    source for later).
12. The end-to-end test answers the probes from a file (e2etest hooks), so it covers the real
    daemon, peer table, meter and limiter chain but not a real network's probes.
