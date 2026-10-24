import App from './App.svelte';
import { mount } from 'svelte';
import './style.css';
export default mount(App, { target: document.getElementById('app') });
