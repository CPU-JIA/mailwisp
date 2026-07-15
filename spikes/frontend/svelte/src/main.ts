import { mount } from 'svelte'
import App from './App.svelte'
import '../../shared/style.css'

const target = document.querySelector<HTMLElement>('#app')
if (!target) throw new Error('app root not found')
mount(App, { target })
