# Security notes (round 1)

> **Traffic between the Net-Bond-007 client and server is NOT encrypted or authenticated.**
> This was a deliberate round-1 scope decision. Read this before using it on networks you don't trust.

## What is protected today

- **Your HTTPS, QUIC, SSH and VPN traffic stays encrypted end-to-end.** Net-Bond-007 forwards those bytes untouched; the server never sees inside them.
- **Joining needs a token.** HELLO frames carry a shared access token, compared in constant time. Wrong tokens get `REJECT bad-token`.
- **Anti-spoofing.** A client may only send from the tunnel address it was assigned.
- **Egress policy.** By default clients cannot reach private, loopback, link-local (including cloud metadata at `169.254.169.254`), CGNAT or multicast addresses through the server (`egress.allowPrivate`).
- **Dashboard.** It listens on `127.0.0.1` by default. Optional password login uses HMAC cookies (HttpOnly, SameSite=Strict), with a delay after a wrong password. The UI warns when it is reachable from the network without a password. On OpenWrt it listens on the LAN; set a password.

## What is NOT protected yet

- **Plaintext in the tunnel.** Anyone on the path between client and server (café Wi-Fi, carrier, the server's ISP) can read unencrypted inner traffic, such as plain HTTP and DNS.
- **The token is sent in clear** inside HELLO, so an on-path observer can learn it.
- **No per-packet authentication.** After a session exists, someone who sees its session id could inject DATA frames. Injected packets must still carry the client's tunnel IP as their source to be accepted.
- **No replay protection** beyond the dedup window.

## Planned (round 2)

Use a Noise IK handshake (WireGuard-style, with the client key pinned to the server) to derive per-session ChaCha20-Poly1305 keys. Every frame gets AEAD sealing with the sequence number as the nonce. The existing dedup window doubles as the replay window. Cost: 16-byte tag per frame, about 1 µs per packet on modern CPUs.
