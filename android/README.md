# Bond007 Relay (Android)

Shares the phone's cellular data with the Bond007 router as one more bonded
link. The phone joins the router's Wi-Fi; the app copies Bond007 frames
between the router (Wi-Fi) and the Bond007 server (cellular) without looking
inside them. The router's bond engine sees an ordinary link, and the server
sees that link arrive from the carrier's address.

```
router link socket --Wi-Fi--> phone :7008 --cellular--> server :7007
router link socket <--Wi-Fi-- phone :7008 <--cellular-- server :7007
```

Status: test build. The router side (relay links) is not written yet; until it
is, the app shows "Waiting for the router".

## Build and install

One-time SDK setup (installs into `~/Android/Sdk`; `local.properties` points there).
`sdkmanager` now forwards to Google's new `android` CLI, which accepts the SDK
terms itself; add `--no-metrics` to opt out of its usage reporting:

```bash
/opt/android-sdk/cmdline-tools/latest/bin/android --sdk=$HOME/Android/Sdk sdk install platforms/android-35 build-tools/35.0.0 platform-tools
```

On the phone: Developer options, USB debugging on, plug in, accept the prompt.

```bash
./gradlew installDebug
```

The first build downloads Gradle 8.13 and the Android Gradle plugin.

## Using it

1. Join the router's Wi-Fi with mobile data switched on.
2. Enter the pairing token from the router dashboard. Leave the router address
   blank to use the Wi-Fi gateway.
3. Tap **Turn off battery optimisation**, then **Start**. A notification stays
   up while the relay runs.

Logs: `adb logcat -s Bond007Relay`.

## Relay protocol (what the router must implement)

All frames use the Bond007 8-byte header (`internal/proto`). Two new control
types travel only between phone and router; the phone forwards every other
frame unchanged.

| Type | Name | Direction | Body (JSON) |
|---|---|---|---|
| 10 | `RELAY_HELLO` | phone :7008 -> router :7008, every 2 s | `token`, `name`, `version`, `model`, `cellular` (bool), `battery` (%), `charging`, `tempC`, `thermal` (0-6), `signal` (0-4), `carrier`, `upBytes`, `downBytes` |
| 11 | `RELAY_ACK` | router -> phone :7008 | `ok` (bool), `reason` (when refused), `server` (`"host:port"`) |

Router behaviour:

- On a valid `RELAY_HELLO` (token matches), register or refresh relay `name` at
  the hello's source address and reply `RELAY_ACK {ok:true, server}`. Bad token:
  `{ok:false, reason:"bad token"}`. Drop the relay after ~10 s without hellos.
- Add a link per relay (kind `cellular`, name e.g. `relay-<name>`). Its socket
  is a plain UDP socket connected to the phone's `ip:7008` over the LAN instead
  of the server. Send it bond frames exactly as on any other link.
- Ignore a relay whose hello says `cellular: false`.

Phone behaviour:

- Accepts frames only from the router's address, and forwards only replies
  from the resolved server address.
- Sends to the server from a socket pinned to the cellular network, never the
  default network (the phone's Wi-Fi is the bonded LAN, so that would loop).
- Resolves the server name over cellular so IPv6-only carriers get a NAT64
  address.
- Replies from the server go back to the source address of the last bond frame
  from the router (the router's link socket).
