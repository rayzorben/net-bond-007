import '@fontsource-variable/inter'
import '@fontsource-variable/jetbrains-mono'
import './app.css'
import { mount } from 'svelte'
import App from './App.svelte'
import { initTheme } from './lib/store.svelte'

initTheme()
const app = mount(App, { target: document.getElementById('app')! })

export default app
