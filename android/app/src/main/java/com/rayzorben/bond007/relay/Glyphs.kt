package com.rayzorben.bond007.relay

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/*
 * Line icons drawn with the canvas, in the style of the dashboard's Lucide
 * set. Each draws into a square of side s centred on c.
 */

private fun DrawScope.stroke(s: Float) = Stroke(width = s * 0.085f, cap = StrokeCap.Round)

fun DrawScope.glyphRouter(c: Offset, s: Float, col: Color) {
    val w = s * 0.78f
    val h = s * 0.32f
    val top = c.y + s * 0.02f
    drawRoundRect(col, Offset(c.x - w / 2, top), Size(w, h), CornerRadius(h * 0.3f), style = stroke(s))
    // antennas
    drawLine(col, Offset(c.x - w * 0.28f, top), Offset(c.x - w * 0.4f, top - s * 0.3f), s * 0.085f, StrokeCap.Round)
    drawLine(col, Offset(c.x + w * 0.28f, top), Offset(c.x + w * 0.4f, top - s * 0.3f), s * 0.085f, StrokeCap.Round)
    // status lights
    for (i in 0..2) drawCircle(col, s * 0.045f, Offset(c.x - w * 0.25f + i * w * 0.16f, top + h / 2))
}

fun DrawScope.glyphPhone(c: Offset, s: Float, col: Color) {
    val w = s * 0.46f
    val h = s * 0.8f
    drawRoundRect(col, Offset(c.x - w / 2, c.y - h / 2), Size(w, h), CornerRadius(w * 0.22f), style = stroke(s))
    drawLine(col, Offset(c.x - w * 0.14f, c.y + h * 0.36f), Offset(c.x + w * 0.14f, c.y + h * 0.36f), s * 0.07f, StrokeCap.Round)
}

fun DrawScope.glyphGlobe(c: Offset, s: Float, col: Color) {
    val r = s * 0.4f
    val st = stroke(s)
    drawCircle(col, r, c, style = st)
    drawOval(col, Offset(c.x - r * 0.45f, c.y - r), Size(r * 0.9f, r * 2), style = st)
    drawLine(col, Offset(c.x - r, c.y), Offset(c.x + r, c.y), s * 0.085f)
}

fun DrawScope.glyphWifi(c: Offset, s: Float, col: Color) {
    val st = stroke(s)
    for (i in 1..3) {
        val r = s * 0.15f * i
        drawArc(col, 225f, 90f, false, Offset(c.x - r, c.y + s * 0.25f - r), Size(r * 2, r * 2), style = st)
    }
    drawCircle(col, s * 0.06f, Offset(c.x, c.y + s * 0.25f))
}

fun DrawScope.glyphCell(c: Offset, s: Float, col: Color) {
    val st = stroke(s)
    drawLine(col, Offset(c.x, c.y - s * 0.1f), Offset(c.x, c.y + s * 0.4f), s * 0.085f, StrokeCap.Round)
    for (i in 1..2) {
        val r = s * 0.17f * i
        drawArc(col, 135f, 90f, false, Offset(c.x - r, c.y - s * 0.1f - r), Size(r * 2, r * 2), style = st)
        drawArc(col, -45f, 90f, false, Offset(c.x - r, c.y - s * 0.1f - r), Size(r * 2, r * 2), style = st)
    }
    drawCircle(col, s * 0.06f, Offset(c.x, c.y - s * 0.1f))
}

fun DrawScope.glyphCheck(c: Offset, s: Float, col: Color) {
    val p = Path().apply {
        moveTo(c.x - s * 0.3f, c.y)
        lineTo(c.x - s * 0.08f, c.y + s * 0.22f)
        lineTo(c.x + s * 0.32f, c.y - s * 0.22f)
    }
    drawPath(p, col, style = stroke(s))
}

fun DrawScope.glyphKey(c: Offset, s: Float, col: Color) {
    val st = stroke(s)
    drawCircle(col, s * 0.18f, Offset(c.x - s * 0.18f, c.y), style = st)
    drawLine(col, Offset(c.x, c.y), Offset(c.x + s * 0.38f, c.y), s * 0.085f, StrokeCap.Round)
    drawLine(col, Offset(c.x + s * 0.28f, c.y), Offset(c.x + s * 0.28f, c.y + s * 0.16f), s * 0.085f, StrokeCap.Round)
}

@Composable
fun Glyph(size: Dp, col: Color, modifier: Modifier = Modifier, draw: DrawScope.(Offset, Float, Color) -> Unit) {
    Canvas(modifier.size(size)) { draw(center, this.size.minDimension, col) }
}

/** Four rising bars, lit up to [level] (0..4; -1 unknown). */
@Composable
fun SignalBars(level: Int, col: Color, modifier: Modifier = Modifier, size: Dp = 22.dp) {
    Canvas(modifier.size(size)) {
        val n = 4
        val gap = this.size.width * 0.08f
        val bw = (this.size.width - gap * (n - 1)) / n
        for (i in 0 until n) {
            val h = this.size.height * (0.3f + 0.7f * (i + 1) / n)
            val lit = level > i
            drawRoundRect(
                if (lit) col else C.line2,
                Offset(i * (bw + gap), this.size.height - h), Size(bw, h), CornerRadius(bw * 0.3f),
            )
        }
    }
}

/** A battery outline filled to [pct], with a bolt while charging. */
@Composable
fun BatteryGauge(pct: Int, charging: Boolean, modifier: Modifier = Modifier, width: Dp = 34.dp) {
    val col = when {
        pct < 0 -> C.faint
        charging -> C.ok
        pct <= 15 -> C.bad
        pct <= 30 -> C.warn
        else -> C.ok
    }
    Canvas(modifier.size(width, width * 0.5f)) {
        val nub = this.size.width * 0.07f
        val bw = this.size.width - nub
        val h = this.size.height
        val sw = h * 0.1f
        drawRoundRect(C.line2, Offset(sw / 2, sw / 2), Size(bw - sw, h - sw), CornerRadius(h * 0.22f), style = Stroke(sw))
        drawRoundRect(C.line2, Offset(bw, h * 0.32f), Size(nub, h * 0.36f), CornerRadius(nub * 0.4f))
        if (pct > 0) {
            val inset = sw * 2f
            val fw = (bw - inset * 2) * pct.coerceIn(0, 100) / 100f
            drawRoundRect(col, Offset(inset, inset), Size(fw, h - inset * 2), CornerRadius(h * 0.12f))
        }
        if (charging) {
            val cx = bw / 2
            val p = Path().apply {
                moveTo(cx + h * 0.08f, h * 0.12f)
                lineTo(cx - h * 0.18f, h * 0.55f)
                lineTo(cx + h * 0.02f, h * 0.55f)
                lineTo(cx - h * 0.08f, h * 0.88f)
                lineTo(cx + h * 0.18f, h * 0.45f)
                lineTo(cx - h * 0.02f, h * 0.45f)
                close()
            }
            drawPath(p, Color.White)
        }
    }
}
