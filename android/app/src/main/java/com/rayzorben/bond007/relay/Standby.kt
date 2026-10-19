package com.rayzorben.bond007.relay

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.offset
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.delay
import kotlin.random.Random

/**
 * Keep-awake standby: a black screen (almost no power on OLED) with a small
 * status line that moves every minute so nothing burns in. The screen stays
 * on, so Android keeps the phone's Wi-Fi awake. Any touch wakes it.
 */
@Composable
fun Standby() {
    var title by remember { mutableStateOf(Status.state().first) }
    var tone by remember { mutableStateOf(Status.state().second) }
    var spot by remember { mutableIntStateOf(0) }
    LaunchedEffect(Unit) {
        var n = 0
        while (true) {
            val (t, s) = Status.state()
            title = t; tone = s
            if (n++ % 60 == 0) spot = Random.nextInt(1000)
            delay(1000)
        }
    }
    BoxWithConstraints(Modifier.fillMaxSize().background(Color.Black)) {
        val x = (maxWidth - 220.dp) * ((spot % 100) / 100f)
        val y = (maxHeight - 80.dp) * ((spot / 100) / 10f)
        Box(Modifier.offset(x, y)) {
            Column(horizontalAlignment = Alignment.Start) {
                Text(title, color = C.tone(tone).copy(alpha = 0.55f), fontSize = 15.sp)
                Text("Keeping Wi-Fi awake · tap to wake", color = Color.White.copy(alpha = 0.3f), fontSize = 11.sp)
            }
        }
    }
}
