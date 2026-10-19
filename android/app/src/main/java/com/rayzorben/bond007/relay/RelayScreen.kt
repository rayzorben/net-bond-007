package com.rayzorben.bond007.relay

import android.os.Build
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.draw.scale
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.input.KeyboardCapitalization
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import java.util.Locale

/** Everything the screen shows, sampled once a second from [Status]. */
private data class View(
    val title: String = "Stopped",
    val tone: Status.Tone = Status.Tone.IDLE,
    val running: Boolean = false,
    val wifi: Boolean = false,
    val cellular: Boolean = false,
    val router: String? = null,
    val server: String? = null,
    val ackAgeMs: Long = -1,
    val refused: String? = null,
    val error: String? = null,
    val up: Float = 0f,
    val down: Float = 0f,
    val upSeries: FloatArray = FloatArray(0),
    val downSeries: FloatArray = FloatArray(0),
    val bytes: Long = 0,
    val packets: Long = 0,
    val dropped: Long = 0,
    val battery: Int = -1,
    val charging: Boolean = false,
    val tempC: Double = Double.NaN,
    val thermal: Int = 0,
    val signal: Int = -1,
    val carrier: String = "",
    val exempt: Boolean = true,
    val awake: Boolean = false,
)

private fun sample(exempt: Boolean): View {
    val now = System.currentTimeMillis()
    val (up, down) = History.sample(now)
    val (title, tone) = Status.state(now)
    return View(
        title = title, tone = tone, running = Status.running, wifi = Status.wifi, cellular = Status.cellular,
        router = Status.router, server = Status.server,
        ackAgeMs = if (Status.lastAckMs == 0L) -1 else now - Status.lastAckMs,
        refused = Status.refused, error = Status.error,
        up = up, down = down, upSeries = History.series(History.up), downSeries = History.series(History.down),
        bytes = Status.upBytes.get() + Status.downBytes.get(),
        packets = Status.upPackets.get() + Status.downPackets.get(), dropped = Status.dropped.get(),
        battery = Status.battery, charging = Status.charging, tempC = Status.tempC, thermal = Status.thermal,
        signal = Status.signal, carrier = Status.carrier, exempt = exempt, awake = Status.awake,
    )
}

@Composable
fun RelayScreen(
    isExempt: () -> Boolean,
    onStart: () -> Unit,
    onStop: () -> Unit,
    onFixBattery: () -> Unit,
) {
    val ctx = LocalContext.current
    var v by remember { mutableStateOf(sample(isExempt())) }
    LaunchedEffect(Unit) {
        while (true) {
            v = sample(isExempt())
            delay(1000)
        }
    }
    val saved = remember { Settings.load(ctx) }
    var token by remember { mutableStateOf(saved.token) }
    var routerHost by remember { mutableStateOf(saved.routerHost) }
    var name by remember { mutableStateOf(saved.name) }
    var keepAwake by remember { mutableStateOf(saved.keepAwake) }
    var onlyCharging by remember { mutableStateOf(saved.keepAwakeOnlyCharging) }
    var settingsOpen by remember { mutableStateOf(saved.token.isEmpty()) }
    fun save() = Settings.save(
        ctx, saved.copy(
            token = token.trim(), routerHost = routerHost.trim(), name = name.trim().ifEmpty { Build.MODEL },
            keepAwake = keepAwake, keepAwakeOnlyCharging = onlyCharging,
        ),
    )

    val up by animateFloatAsState(v.up, tween(800), label = "up")
    val down by animateFloatAsState(v.down, tween(800), label = "down")

    Box(
        Modifier.fillMaxSize().background(C.bg).drawBehind {
            // The dashboard's violet haze at the top.
            drawCircle(
                Brush.radialGradient(listOf(C.accent.copy(alpha = 0.22f), Color.Transparent), Offset(size.width * 0.3f, 0f), size.width),
                size.width, Offset(size.width * 0.3f, 0f),
            )
            drawCircle(
                Brush.radialGradient(listOf(C.cellular.copy(alpha = 0.10f), Color.Transparent), Offset(size.width, size.height * 0.15f), size.width * 0.8f),
                size.width * 0.8f, Offset(size.width, size.height * 0.15f),
            )
        },
    ) {
        Column(
            Modifier.fillMaxSize().verticalScroll(rememberScrollState())
                .windowInsetsPadding(WindowInsets.safeDrawing).padding(horizontal = 16.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Header(v)
            Hero(v, up, down)
            PowerButton(v.running) { if (v.running) onStop() else { save(); onStart() } }

            v.refused?.let { Banner("The router refused this phone: $it", C.bad) }
            if (v.refused == null) v.error?.let { if (v.running) Banner(it, C.warn) }
            if (!v.exempt) Banner("Battery optimisation can pause the relay in the background.", C.warn, "Fix", onFixBattery)
            if (v.running && !keepAwake) {
                Banner(
                    "With this screen off, Android puts Wi-Fi to sleep and holds traffic for up to 2 s, so calls through the phone stall. Turn on Keep awake and leave the app open.",
                    C.warn, "Turn on",
                ) { keepAwake = true; save() }
            } else if (v.running && onlyCharging && !v.charging) {
                Banner(
                    "On battery the screen sleeps as usual, so Android naps Wi-Fi and calls through the phone can stall for up to 2 s. Plug in to keep it awake.",
                    C.warn,
                )
            }

            Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                RateCard("Upload", "↑", up, v.upSeries, C.cellular, Modifier.weight(1f))
                RateCard("Download", "↓", down, v.downSeries, C.wifi, Modifier.weight(1f))
            }
            Panel {
                Row {
                    MiniStat("Relayed", size(v.bytes), Modifier.weight(1f))
                    MiniStat("Packets", count(v.packets), Modifier.weight(1f))
                    MiniStat("Dropped", count(v.dropped), Modifier.weight(1f), if (v.dropped > 0) C.warn else C.text)
                }
            }
            PhoneCard(v)
            Checklist(v)
            SettingsCard(
                open = settingsOpen, onToggle = { settingsOpen = !settingsOpen },
                token = token, onToken = { token = it },
                router = routerHost, onRouter = { routerHost = it },
                name = name, onName = { name = it },
                keepAwake = keepAwake, onKeepAwake = { keepAwake = it; save() },
                onlyCharging = onlyCharging, onOnlyCharging = { onlyCharging = it; save() },
                running = v.running, onApply = { save(); onStart() },
            )
            Text(
                "Net-Bond-007 Relay ${BuildConfig.VERSION_NAME} · ${Build.MANUFACTURER} ${Build.MODEL}",
                color = C.faint, fontSize = 11.sp, modifier = Modifier.align(Alignment.CenterHorizontally).padding(vertical = 8.dp),
            )
        }
    }
}

@Composable
private fun Header(v: View) {
    Row(Modifier.fillMaxWidth().padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
        Box(
            Modifier.size(40.dp).clip(RoundedCornerShape(12.dp))
                .background(Brush.linearGradient(listOf(C.accent, C.cellular))),
            contentAlignment = Alignment.Center,
        ) { Text("007", color = Color.White, fontSize = 13.sp, fontWeight = FontWeight.Black, style = Num) }
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Caps("Net-Bond-007", color = C.accent2)
            Text("Phone Relay", color = C.text, fontSize = 22.sp, fontWeight = FontWeight.Bold)
        }
        Pill(
            when (v.tone) {
                Status.Tone.OK -> "Live"
                Status.Tone.IDLE -> "Off"
                Status.Tone.BAD -> "Refused"
                Status.Tone.WARN -> "Connecting"
            },
            v.tone,
        )
    }
}

@Composable
private fun Hero(v: View, up: Float, down: Float) {
    val tone = C.tone(v.tone)
    Panel(accent = if (v.tone == Status.Tone.OK) C.accent else null) {
        FlowDiagram(
            running = v.running, wifi = v.wifi, paired = v.ackAgeMs in 0..6000 && v.refused == null,
            cellular = v.cellular, serverKnown = v.server != null, upBps = up, downBps = down,
        )
        Row(Modifier.fillMaxWidth()) {
            NodeLabel("Router", v.router ?: "—", Modifier.weight(1f))
            NodeLabel("This phone", v.carrier.ifEmpty { "Mobile data" }, Modifier.weight(1f), bright = true)
            NodeLabel("Server", v.server?.substringBeforeLast(':') ?: "—", Modifier.weight(1f))
        }
        Spacer(Modifier.height(14.dp))
        Text(v.title, color = tone, fontSize = 19.sp, fontWeight = FontWeight.Bold, modifier = Modifier.align(Alignment.CenterHorizontally))
        Text(
            subtitle(v), color = C.muted, fontSize = 13.sp,
            modifier = Modifier.align(Alignment.CenterHorizontally).padding(top = 2.dp),
        )
    }
}

private fun subtitle(v: View): String = when {
    !v.running -> "Share this phone's mobile data with the router"
    v.tone == Status.Tone.OK -> "Bonding ${v.carrier.ifEmpty { "mobile data" }} into the router's connection"
    !v.wifi -> "Join the router's Wi-Fi"
    !v.cellular -> "Turn on mobile data"
    v.refused != null -> "Check the pairing token"
    v.ackAgeMs < 0 || v.ackAgeMs > 6000 -> "Is \"Accept phone relays\" on in the router's settings?"
    else -> "Resolving the server over mobile data"
}

@Composable
private fun NodeLabel(title: String, sub: String, modifier: Modifier, bright: Boolean = false) {
    Column(modifier, horizontalAlignment = Alignment.CenterHorizontally) {
        Text(title, color = if (bright) C.text else C.text2, fontSize = 12.5.sp, fontWeight = FontWeight.SemiBold)
        Text(sub, color = C.muted, fontSize = 11.sp, maxLines = 1, style = Num.copy(fontWeight = FontWeight.Normal))
    }
}

@Composable
private fun PowerButton(running: Boolean, onClick: () -> Unit) {
    val src = remember { MutableInteractionSource() }
    val pressed by src.collectIsPressedAsState()
    val scale by animateFloatAsState(if (pressed) 0.97f else 1f, label = "press")
    val border by animateColorAsState(if (running) C.bad.copy(alpha = 0.5f) else Color.Transparent, label = "border")
    Box(
        Modifier.fillMaxWidth().height(58.dp).scale(scale).clip(CircleShape)
            .background(
                if (running) Brush.linearGradient(listOf(C.panel2, C.panel2))
                else Brush.linearGradient(listOf(C.accent, Color(0xFFC04BD0), C.cellular)),
            )
            .border(1.dp, border, CircleShape)
            .clickable(interactionSource = src, indication = null, onClick = onClick),
        contentAlignment = Alignment.Center,
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Glyph(18.dp, if (running) C.bad else Color.White) { c, s, col ->
                // power symbol
                drawArc(col, -60f, 300f, false, Offset(c.x - s * 0.36f, c.y - s * 0.3f),
                    androidx.compose.ui.geometry.Size(s * 0.72f, s * 0.72f),
                    style = androidx.compose.ui.graphics.drawscope.Stroke(s * 0.11f, cap = androidx.compose.ui.graphics.StrokeCap.Round))
                drawLine(col, Offset(c.x, c.y - s * 0.45f), Offset(c.x, c.y + s * 0.02f), s * 0.11f, androidx.compose.ui.graphics.StrokeCap.Round)
            }
            Spacer(Modifier.width(10.dp))
            Text(
                if (running) "Stop relay" else "Start relaying",
                color = if (running) C.bad else Color.White, fontSize = 16.sp, fontWeight = FontWeight.Bold,
            )
        }
    }
}

@Composable
private fun PhoneCard(v: View) {
    Panel {
        Caps("This phone")
        Spacer(Modifier.height(12.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.Bottom) {
                    SignalBars(v.signal, C.cellular)
                    Spacer(Modifier.width(8.dp))
                    Text(if (v.signal >= 0) "${v.signal}/4" else "—", style = Num, fontSize = 17.sp, color = C.text)
                }
                Text(v.carrier.ifEmpty { "Signal" }, color = C.muted, fontSize = 12.sp, maxLines = 1)
            }
            Column(Modifier.weight(1f)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    BatteryGauge(v.battery, v.charging)
                    Spacer(Modifier.width(8.dp))
                    Text(if (v.battery >= 0) "${v.battery}%" else "—", style = Num, fontSize = 17.sp, color = C.text)
                }
                Text(if (v.charging) "Charging" else "Battery", color = C.muted, fontSize = 12.sp)
            }
            Column(Modifier.weight(1f)) {
                val (word, col) = when {
                    v.thermal >= 3 -> "Hot" to C.bad
                    v.thermal == 2 -> "Warm" to C.warn
                    else -> "Cool" to C.ok
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(
                        if (v.tempC.isNaN()) "—" else String.format(Locale.US, "%.0f°", v.tempC),
                        style = Num, fontSize = 17.sp, color = C.text,
                    )
                    Spacer(Modifier.width(8.dp))
                    Text(
                        word, color = col, fontSize = 11.sp, fontWeight = FontWeight.Bold,
                        modifier = Modifier.clip(CircleShape).background(col.copy(alpha = 0.14f)).padding(horizontal = 8.dp, vertical = 2.dp),
                    )
                }
                Text(if (v.thermal >= 3) "Throttling" else "Temperature", color = C.muted, fontSize = 12.sp)
            }
        }
    }
}

@Composable
private fun Checklist(v: View) {
    val idle = !v.running
    fun t(ok: Boolean, bad: Boolean = false) = when {
        idle -> Status.Tone.IDLE
        bad -> Status.Tone.BAD
        ok -> Status.Tone.OK
        else -> Status.Tone.WARN
    }
    val paired = v.ackAgeMs in 0..6000 && v.refused == null
    Panel {
        Caps("Connection")
        Spacer(Modifier.height(6.dp))
        CheckRow("Wi-Fi", if (v.wifi) "Router at ${v.router ?: "?"}" else "Not on the router's Wi-Fi", t(v.wifi)) { c, s, col -> glyphWifi(c, s, col) }
        CheckRow("Mobile data", if (v.cellular) v.carrier.ifEmpty { "Connected" } else "Unavailable", t(v.cellular)) { c, s, col -> glyphCell(c, s, col) }
        CheckRow(
            "Router pairing",
            when {
                v.refused != null -> v.refused
                paired -> "Paired · last reply ${v.ackAgeMs / 1000}s ago"
                else -> "No reply yet"
            },
            t(paired, v.refused != null),
        ) { c, s, col -> glyphKey(c, s, col) }
        CheckRow("Server", v.server ?: "Waiting for the router", t(v.server != null)) { c, s, col -> glyphGlobe(c, s, col) }
    }
}

@Composable
private fun SettingsCard(
    open: Boolean, onToggle: () -> Unit,
    token: String, onToken: (String) -> Unit,
    router: String, onRouter: (String) -> Unit,
    name: String, onName: (String) -> Unit,
    keepAwake: Boolean, onKeepAwake: (Boolean) -> Unit,
    onlyCharging: Boolean, onOnlyCharging: (Boolean) -> Unit,
    running: Boolean, onApply: () -> Unit,
) {
    val turn by animateFloatAsState(if (open) 180f else 0f, label = "chevron")
    Panel {
        Row(Modifier.fillMaxWidth().clickable(onClick = onToggle), verticalAlignment = Alignment.CenterVertically) {
            Caps("Settings", Modifier.weight(1f))
            if (token.isEmpty()) {
                Text("Needs token", color = C.warn, fontSize = 11.sp, fontWeight = FontWeight.Bold)
                Spacer(Modifier.width(8.dp))
            }
            Glyph(16.dp, C.muted, Modifier.rotate(turn)) { c, s, col ->
                drawLine(col, Offset(c.x - s * 0.3f, c.y - s * 0.12f), Offset(c.x, c.y + s * 0.18f), s * 0.12f, androidx.compose.ui.graphics.StrokeCap.Round)
                drawLine(col, Offset(c.x + s * 0.3f, c.y - s * 0.12f), Offset(c.x, c.y + s * 0.18f), s * 0.12f, androidx.compose.ui.graphics.StrokeCap.Round)
            }
        }
        AnimatedVisibility(open) {
            Column(Modifier.padding(top = 12.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Field("Pairing token", token, onToken, "From the router's Phone relays settings", KeyboardCapitalization.None)
                Field("Router address", router, onRouter, "Blank = the Wi-Fi gateway", KeyboardCapitalization.None)
                Field("Link name", name, onName, "Shown on the router dashboard", KeyboardCapitalization.Words)
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("Keep awake", color = C.text, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                        Text(
                            "While relaying with this app open, keep the screen on (it fades to black after 20 s). Android only keeps Wi-Fi fast while the app is on screen.",
                            color = C.muted, fontSize = 12.sp,
                        )
                    }
                    Spacer(Modifier.width(12.dp))
                    Switch(
                        keepAwake, onKeepAwake,
                        colors = SwitchDefaults.colors(checkedTrackColor = C.accent, checkedThumbColor = Color.White),
                    )
                }
                if (keepAwake) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text("Only while charging", color = C.text, fontSize = 14.sp, fontWeight = FontWeight.SemiBold)
                            Text(
                                "On battery, let the screen sleep as usual (calls through the phone may stall).",
                                color = C.muted, fontSize = 12.sp,
                            )
                        }
                        Spacer(Modifier.width(12.dp))
                        Switch(
                            onlyCharging, onOnlyCharging,
                            colors = SwitchDefaults.colors(checkedTrackColor = C.accent, checkedThumbColor = Color.White),
                        )
                    }
                }
                if (running) {
                    Text(
                        "Apply and restart", color = C.accent2, fontSize = 13.sp, fontWeight = FontWeight.Bold,
                        modifier = Modifier.clip(CircleShape).background(C.accent.copy(alpha = 0.14f)).clickable(onClick = onApply)
                            .padding(horizontal = 14.dp, vertical = 8.dp),
                    )
                }
            }
        }
    }
}

@Composable
private fun Field(label: String, value: String, onChange: (String) -> Unit, hint: String, caps: KeyboardCapitalization) {
    OutlinedTextField(
        value, onChange, Modifier.fillMaxWidth(),
        label = { Text(label) },
        placeholder = { Text(hint, color = C.faint) },
        singleLine = true,
        shape = RoundedCornerShape(12.dp),
        keyboardOptions = KeyboardOptions(capitalization = caps, autoCorrectEnabled = false, imeAction = ImeAction.Next),
        colors = OutlinedTextFieldDefaults.colors(
            focusedBorderColor = C.accent, unfocusedBorderColor = C.line2,
            focusedLabelColor = C.accent2, unfocusedLabelColor = C.muted,
            focusedContainerColor = C.bg2, unfocusedContainerColor = C.bg2,
            cursorColor = C.accent2, focusedTextColor = C.text, unfocusedTextColor = C.text,
        ),
    )
}
