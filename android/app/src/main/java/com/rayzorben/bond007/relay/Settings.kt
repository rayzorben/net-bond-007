package com.rayzorben.bond007.relay

import android.content.Context
import android.os.Build

data class Settings(
    /** Pairing token; must match the router's relay token. */
    val token: String,
    /** Router address. Blank = the Wi-Fi default gateway. */
    val routerHost: String,
    /** Router UDP port that receives RELAY_HELLO. */
    val routerPort: Int,
    /** Phone UDP port the router sends bond frames to. */
    val relayPort: Int,
    /** Link name shown on the router dashboard. */
    val name: String,
    /**
     * Keep the screen on (dimmed to black) while relaying. Off screen, Android
     * puts the phone's Wi-Fi in power save and holds traffic for up to ~2 s,
     * which stalls calls through the relay; the low-latency Wi-Fi lock only
     * works while this app is on screen.
     */
    val keepAwake: Boolean = true,
    /** Keep awake only while the phone is plugged in (on battery it sleeps as usual). */
    val keepAwakeOnlyCharging: Boolean = true,
) {
    companion object {
        const val DEFAULT_PORT = 7008

        fun load(ctx: Context): Settings {
            val p = ctx.getSharedPreferences("relay", Context.MODE_PRIVATE)
            return Settings(
                token = p.getString("token", "") ?: "",
                routerHost = p.getString("routerHost", "") ?: "",
                routerPort = p.getInt("routerPort", DEFAULT_PORT),
                relayPort = p.getInt("relayPort", DEFAULT_PORT),
                name = p.getString("name", null) ?: Build.MODEL,
                keepAwake = p.getBoolean("keepAwake", true),
                keepAwakeOnlyCharging = p.getBoolean("keepAwakeOnlyCharging", true),
            )
        }

        fun save(ctx: Context, s: Settings) {
            ctx.getSharedPreferences("relay", Context.MODE_PRIVATE).edit()
                .putString("token", s.token)
                .putString("routerHost", s.routerHost)
                .putInt("routerPort", s.routerPort)
                .putInt("relayPort", s.relayPort)
                .putString("name", s.name)
                .putBoolean("keepAwake", s.keepAwake)
                .putBoolean("keepAwakeOnlyCharging", s.keepAwakeOnlyCharging)
                .apply()
        }
    }
}
