package com.rayzorben.bond007.relay

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp
import java.util.Locale

/** The Net-Bond-007 dashboard palette (web/src/app.css). */
object C {
    val bg = Color(0xFF090B10)
    val bg2 = Color(0xFF0D1017)
    val panel = Color(0xFF12161F)
    val panel2 = Color(0xFF171C27)
    val panel3 = Color(0xFF1E2431)
    val line = Color(0xFF242B3A)
    val line2 = Color(0xFF2F3748)
    val text = Color(0xFFE8EBF2)
    val text2 = Color(0xFFB4BCCC)
    val muted = Color(0xFF7D879B)
    val faint = Color(0xFF545D70)

    val accent = Color(0xFF8B5CF6)
    val accent2 = Color(0xFFA78BFA)
    val cyan = Color(0xFF22D3EE)
    val gold = Color(0xFFFBBF24)
    val ok = Color(0xFF22C55E)
    val bad = Color(0xFFFB4F6E)
    val warn = Color(0xFFF5A524)
    val info = Color(0xFF38BDF8)

    val wifi = Color(0xFFA78BFA)
    val cellular = Color(0xFFF472B6)
    val ethernet = Color(0xFF22D3EE)

    fun tone(t: Status.Tone) = when (t) {
        Status.Tone.OK -> ok
        Status.Tone.WARN -> warn
        Status.Tone.BAD -> bad
        Status.Tone.IDLE -> muted
    }
}

/** Tabular figures so changing numbers don't jitter. */
val Num = TextStyle(fontFeatureSettings = "tnum", fontWeight = FontWeight.Bold)
val Label = TextStyle(fontSize = 10.5.sp, fontWeight = FontWeight.SemiBold, letterSpacing = 0.9.sp, color = C.muted)

@Composable
fun RelayTheme(content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = C.accent, onPrimary = Color.White, secondary = C.cellular,
            background = C.bg, surface = C.panel, onSurface = C.text, onBackground = C.text,
            surfaceVariant = C.panel2, onSurfaceVariant = C.text2, outline = C.line2, error = C.bad,
        ),
        content = content,
    )
}

/** Bits per second as (value, unit), e.g. 12.4 / "Mbps". */
fun rate(bps: Float): Pair<String, String> = when {
    bps >= 1e9f -> String.format(Locale.US, "%.2f", bps / 1e9f) to "Gbps"
    bps >= 1e6f -> String.format(Locale.US, if (bps >= 1e8f) "%.0f" else "%.1f", bps / 1e6f) to "Mbps"
    bps >= 1e3f -> String.format(Locale.US, "%.0f", bps / 1e3f) to "kbps"
    else -> String.format(Locale.US, "%.0f", bps) to "bps"
}

fun size(b: Long): String = when {
    b >= 1L shl 30 -> String.format(Locale.US, "%.2f GB", b / (1L shl 30).toDouble())
    b >= 1L shl 20 -> String.format(Locale.US, "%.1f MB", b / (1L shl 20).toDouble())
    b >= 1L shl 10 -> String.format(Locale.US, "%.0f KB", b / 1024.0)
    else -> "$b B"
}

fun count(n: Long): String = when {
    n >= 1_000_000 -> String.format(Locale.US, "%.1fM", n / 1e6)
    n >= 10_000 -> String.format(Locale.US, "%.0fk", n / 1e3)
    else -> n.toString()
}
