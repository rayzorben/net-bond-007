package com.rayzorben.bond007.relay

import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.StrokeJoin
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

private val PanelShape = RoundedCornerShape(16.dp)

/** A dashboard panel: dark glass with a faint top highlight. */
@Composable
fun Panel(modifier: Modifier = Modifier, accent: Color? = null, content: @Composable ColumnScope.() -> Unit) {
    Column(
        modifier
            .clip(PanelShape)
            .background(Brush.verticalGradient(listOf(C.panel2, C.panel)))
            .border(1.dp, accent?.copy(alpha = 0.45f) ?: C.line, PanelShape)
            .drawBehind {
                if (accent != null) drawRect(Brush.verticalGradient(listOf(accent.copy(alpha = 0.10f), Color.Transparent), 0f, size.height * 0.6f))
            }
            .padding(16.dp),
        content = content,
    )
}

/** Small uppercase heading. */
@Composable
fun Caps(text: String, modifier: Modifier = Modifier, color: Color = C.muted) {
    Text(text.uppercase(), modifier, style = Label.copy(color = color))
}

/** A status chip with a (pulsing, when live) dot. */
@Composable
fun Pill(text: String, tone: Status.Tone, modifier: Modifier = Modifier) {
    val col = C.tone(tone)
    val pulse by rememberInfiniteTransition(label = "pill").animateFloat(
        0.35f, 1f, infiniteRepeatable(tween(900), RepeatMode.Reverse), label = "dot",
    )
    Row(
        modifier.clip(CircleShape).background(col.copy(alpha = 0.13f)).padding(horizontal = 10.dp, vertical = 5.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.size(7.dp).clip(CircleShape).background(col.copy(alpha = if (tone == Status.Tone.OK) pulse else 1f)))
        Spacer(Modifier.width(6.dp))
        Text(text, color = col, fontSize = 12.sp, fontWeight = FontWeight.SemiBold, maxLines = 1)
    }
}

/** Throughput card: big animated number and a 60 s sparkline. */
@Composable
fun RateCard(title: String, arrow: String, bps: Float, series: FloatArray, col: Color, modifier: Modifier = Modifier) {
    val (v, unit) = rate(bps)
    Panel(modifier) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(arrow, color = col, fontSize = 13.sp, fontWeight = FontWeight.Bold)
            Spacer(Modifier.width(5.dp))
            Caps(title)
        }
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.Bottom) {
            Text(v, style = Num, fontSize = 28.sp, color = C.text)
            Spacer(Modifier.width(4.dp))
            Text(unit, color = C.muted, fontSize = 12.sp, modifier = Modifier.padding(bottom = 5.dp))
        }
        Spacer(Modifier.height(8.dp))
        Sparkline(series, col, Modifier.fillMaxWidth().height(40.dp))
    }
}

@Composable
fun Sparkline(series: FloatArray, col: Color, modifier: Modifier = Modifier) {
    Canvas(modifier) {
        val n = History.SIZE
        val peak = (series.maxOrNull() ?: 0f).coerceAtLeast(1f)
        val dx = size.width / (n - 1)
        val x0 = size.width - dx * (series.size - 1)
        fun pt(i: Int) = Offset(x0 + dx * i, size.height - 2f - (size.height - 4f) * series[i] / peak)
        // baseline
        drawLine(C.line, Offset(0f, size.height - 1f), Offset(size.width, size.height - 1f), 1f)
        if (series.size < 2) return@Canvas
        val line = Path().apply { moveTo(pt(0).x, pt(0).y); for (i in 1 until series.size) lineTo(pt(i).x, pt(i).y) }
        val area = Path().apply {
            addPath(line)
            lineTo(pt(series.size - 1).x, size.height); lineTo(x0, size.height); close()
        }
        drawPath(area, Brush.verticalGradient(listOf(col.copy(alpha = 0.35f), col.copy(alpha = 0f))))
        drawPath(line, col, style = Stroke(2.dp.toPx(), cap = StrokeCap.Round, join = StrokeJoin.Round))
        drawCircle(col, 3.dp.toPx(), pt(series.size - 1))
    }
}

/** Label over value, for compact stat rows. */
@Composable
fun MiniStat(label: String, value: String, modifier: Modifier = Modifier, color: Color = C.text) {
    Column(modifier) {
        Caps(label)
        Spacer(Modifier.height(3.dp))
        Text(value, style = Num, fontSize = 17.sp, color = color, maxLines = 1)
    }
}

/** A checklist row: glyph in a tinted tile, title, detail, status dot. */
@Composable
fun CheckRow(
    title: String, detail: String, tone: Status.Tone,
    glyph: DrawScope.(Offset, Float, Color) -> Unit,
) {
    val col = C.tone(tone)
    Row(Modifier.fillMaxWidth().padding(vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(
            Modifier.size(36.dp).clip(RoundedCornerShape(10.dp)).background(col.copy(alpha = 0.13f)),
            contentAlignment = Alignment.Center,
        ) { Glyph(20.dp, col, draw = glyph) }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(title, color = C.text, fontSize = 14.5.sp, fontWeight = FontWeight.SemiBold)
            Text(detail, color = C.muted, fontSize = 12.sp, maxLines = 2)
        }
        Box(Modifier.size(9.dp).clip(CircleShape).background(col))
    }
}

/** A tinted notice with an optional action. */
@Composable
fun Banner(text: String, col: Color, action: String? = null, onAction: () -> Unit = {}) {
    Row(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(12.dp)).background(col.copy(alpha = 0.12f))
            .border(1.dp, col.copy(alpha = 0.35f), RoundedCornerShape(12.dp)).padding(horizontal = 14.dp, vertical = 10.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(text, color = col, fontSize = 13.sp, modifier = Modifier.weight(1f))
        if (action != null) {
            Spacer(Modifier.width(10.dp))
            Text(
                action, color = C.bg, fontSize = 12.sp, fontWeight = FontWeight.Bold,
                modifier = Modifier.clip(CircleShape).background(col).clickable(onClick = onAction).padding(horizontal = 12.dp, vertical = 6.dp),
            )
        }
    }
}
