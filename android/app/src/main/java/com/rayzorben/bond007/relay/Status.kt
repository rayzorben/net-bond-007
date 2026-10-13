package com.rayzorben.bond007.relay

import java.util.concurrent.atomic.AtomicLong

/** Live relay state, written by the service and read by the screen. */
object Status {
    @Volatile var running = false
    @Volatile var wifi = false
    @Volatile var cellular = false
    @Volatile var router: String? = null
    @Volatile var server: String? = null
    @Volatile var lastAckMs = 0L
    @Volatile var refused: String? = null
    @Volatile var error: String? = null

    val upBytes = AtomicLong()
    val downBytes = AtomicLong()
    val upPackets = AtomicLong()
    val downPackets = AtomicLong()
    val dropped = AtomicLong()

    fun reset() {
        wifi = false; cellular = false; router = null; server = null
        lastAckMs = 0; refused = null; error = null
        upBytes.set(0); downBytes.set(0); upPackets.set(0); downPackets.set(0); dropped.set(0)
    }

    fun state(now: Long = System.currentTimeMillis()): String = when {
        !running -> "Stopped"
        !wifi -> "Waiting for Wi-Fi"
        !cellular -> "Waiting for cellular data"
        refused != null -> "Router refused: $refused"
        now - lastAckMs > 6000 -> "Waiting for the router (is relay support on?)"
        server == null -> "Waiting for the server address"
        else -> "Relaying"
    }
}
