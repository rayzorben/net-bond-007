package com.rayzorben.bond007.relay

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.drawText
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlin.math.PI
import kotlin.math.log10
import kotlin.math.max
import kotlin.math.sin

/**
 * Router <-Wi-Fi-> this phone <-cellular-> server, like the dashboard's bond
 * map: each hop glows in its link colour, and particles stream along it in
 * both directions, denser and faster as throughput grows. Upload runs above
 * the line (towards the server), download below (towards the router).
 */
@Composable
fun FlowDiagram(
    running: Boolean,
    wifi: Boolean,
    paired: Boolean,
    cellular: Boolean,
    serverKnown: Boolean,
    upBps: Float,
    downBps: Float,
    modifier: Modifier = Modifier,
) {
    val time = remember { mutableFloatStateOf(0f) }
    LaunchedEffect(Unit) {
        val start = withFrameNanos { it }
        while (true) withFrameNanos { time.floatValue = (it - start) / 1e9f }
    }
    val measurer = rememberTextMeasurer()
    val tag = TextStyle(fontSize = 9.5.sp, fontWeight = FontWeight.SemiBold, letterSpacing = 1.2.sp)

    Canvas(modifier.fillMaxWidth().height(150.dp)) {
        val t = time.floatValue // read here: only the drawing repeats each frame
        val y = size.height * 0.5f
        val xs = floatArrayOf(size.width / 6f, size.width / 2f, size.width * 5f / 6f)
        val r = 25.dp.toPx()
        val rp = 31.dp.toPx() // the phone node is the hero

        val hopA = running && wifi && paired
        val hopB = running && cellular && serverKnown
        segment(xs[0] + r, xs[1] - rp, y, C.wifi, hopA, if (hopA && hopB) upBps else 0f, if (hopA && hopB) downBps else 0f, t)
        segment(xs[1] + rp, xs[2] - r, y, C.cellular, hopB, if (hopA && hopB) upBps else 0f, if (hopA && hopB) downBps else 0f, t)

        fun label(text: String, cx: Float, col: Color) {
            val m = measurer.measure(text, tag.copy(color = col))
            drawText(m, topLeft = Offset(cx - m.size.width / 2f, y - 14.dp.toPx() - m.size.height))
        }
        label("WI-FI", (xs[0] + xs[1]) / 2, if (hopA) C.wifi else C.faint)
        label("CELLULAR", (xs[1] + xs[2]) / 2, if (hopB) C.cellular else C.faint)

        val pulse = (sin(t * 2 * PI.toFloat() / 2.4f) + 1) / 2
        node(Offset(xs[0], y), r, if (running && wifi) C.wifi else C.line2, running && paired, pulse) { c, s, col -> glyphRouter(c, s, col) }
        node(Offset(xs[2], y), r, if (hopB) C.cyan else C.line2, hopB, pulse) { c, s, col -> glyphGlobe(c, s, col) }
        // The phone: a gradient ring from Wi-Fi to cellular.
        if (running) {
            drawCircle(
                Brush.radialGradient(listOf(C.accent.copy(alpha = 0.35f * (0.5f + pulse / 2)), Color.Transparent), Offset(xs[1], y), rp * 2.1f),
                rp * 2.1f, Offset(xs[1], y),
            )
        }
        drawCircle(C.panel2, rp, Offset(xs[1], y))
        drawCircle(
            if (running) Brush.sweepGradient(listOf(C.wifi, C.cellular, C.accent, C.wifi), Offset(xs[1], y))
            else Brush.linearGradient(listOf(C.line2, C.line2)),
            rp, Offset(xs[1], y), style = Stroke(2.5.dp.toPx()),
        )
        glyphPhone(Offset(xs[1], y), rp * 1.05f, if (running) C.text else C.muted)
    }
}

private fun DrawScope.node(
    c: Offset, r: Float, col: Color, live: Boolean, pulse: Float,
    glyph: DrawScope.(Offset, Float, Color) -> Unit,
) {
    if (live) {
        drawCircle(col.copy(alpha = 0.10f + 0.12f * pulse), r + 7.dp.toPx() * (0.6f + 0.4f * pulse), c)
    }
    drawCircle(C.panel2, r, c)
    drawCircle(col, r, c, style = Stroke(2.dp.toPx()))
    glyph(c, r * 1.05f, if (live) col else C.muted)
}

/** One hop with its glow and two particle streams. */
private fun DrawScope.segment(x1: Float, x2: Float, y: Float, col: Color, active: Boolean, up: Float, down: Float, t: Float) {
    if (!active) {
        drawLine(
            C.line2, Offset(x1, y), Offset(x2, y), 2.dp.toPx(),
            pathEffect = PathEffect.dashPathEffect(floatArrayOf(6.dp.toPx(), 6.dp.toPx())), cap = StrokeCap.Round,
        )
        return
    }
    drawLine(col.copy(alpha = 0.14f), Offset(x1, y), Offset(x2, y), 14.dp.toPx(), cap = StrokeCap.Round)
    drawLine(
        Brush.horizontalGradient(listOf(col.copy(alpha = 0.35f), col.copy(alpha = 0.8f), col.copy(alpha = 0.35f)), x1, x2),
        Offset(x1, y), Offset(x2, y), 2.dp.toPx(), cap = StrokeCap.Round,
    )
    val lane = 5.dp.toPx()
    stream(x1, x2, y - lane, col, up, t, forward = true)
    stream(x1, x2, y + lane, col, down, t, forward = false)
}

private fun DrawScope.stream(x1: Float, x2: Float, y: Float, col: Color, bps: Float, t: Float, forward: Boolean) {
    if (bps < 2_000f) return
    val n = (2 + 2.2f * log10(bps / 2_000f)).toInt().coerceIn(1, 14)
    val speed = 0.28f + 0.16f * log10(max(bps, 1e3f) / 1e3f) // track lengths per second
    val halo = 6.dp.toPx()
    for (i in 0 until n) {
        var f = (t * speed + i.toFloat() / n + if (forward) 0f else 0.37f) % 1f
        if (!forward) f = 1f - f
        val x = x1 + (x2 - x1) * f
        val fade = (minOf(f, 1 - f) * 8).coerceIn(0f, 1f) // fade in and out at the nodes
        val p = Offset(x, y)
        drawCircle(Brush.radialGradient(listOf(col.copy(alpha = 0.55f * fade), Color.Transparent), p, halo), halo, p)
        drawCircle(Color.White.copy(alpha = 0.9f * fade), 1.8.dp.toPx(), p)
    }
}
