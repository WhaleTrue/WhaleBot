<script>
  import { onMount } from 'svelte';
  import { json, setCSRF, providers as getProviders, nodeID } from './api.js';
  import Chat from './Chat.svelte';
  import Topology from './Topology.svelte';
  import Resource from './Resource.svelte';
  import Models from './Models.svelte';
  import Adapters from './Adapters.svelte';
  import Benchmark from './Benchmark.svelte';
  import Nodes from './Nodes.svelte';
  let user = null;
  let username = 'admin';
  let password = '';
  let error = '';
  let loading = true;
  let topology = null;
  let tab = 'Nodes';
  let currentPassword = '';
  let newPassword = '';
  let accountName = '';
  let accountPassword = '';
  let accountScopes = 'agent.run,session.read,session.write,mesh.inspect';
  let accounts = [];
  let image = '';
  let approvalResult = null;
  let executorNode = '';
  const pages = ['Nodes', 'Chat', 'Topology', 'Sessions', 'Models', 'Adapters', 'Skills', 'Secrets', 'Workspace', 'Artifacts', 'Docker', 'Logs', 'Stats', 'Benchmark', 'Tools', 'Account'];
  $: providers = getProviders(topology);
  $: resources = {
    Sessions: { description: 'Private session history, message context and lifecycle.', matches: c => c.id === 'session.store' },
    Models: { description: 'Inspect advertised model operations and configuration contracts.', matches: c => c.id.startsWith('llm.') && c.id !== 'llm.invoke' },
    Adapters: { description: 'Configure adapter credentials and allowed users. Telegram status distinguishes unconfigured, polling and errors. Tokens are write-only.', matches: c => c.id.startsWith('adapter.') },
    Skills: { description: 'Search, inspect, version, upload and delete skills. File contents and archives use base64; use the file editor below.', matches: c => c.id.startsWith('skills.') },
    Secrets: { description: 'Manage user-owned secret keys and notes. Lists are masked. Raw secret resolution is runtime-only and never exposed through this browser API.', matches: c => c.id.startsWith('secrets.') },
    Workspace: { description: 'List, read, write and delete files within a user-owned workspace. File contents use base64.', matches: c => c.id.startsWith('workspace.') },
    Artifacts: { description: 'Export actual container artifacts and download immutable exports through authenticated capability operations.', matches: c => c.id === 'artifact.export' || c.id === 'container.files' },
    Docker: { description: 'Manage Docker containers, images, deployments, execution and jobs. External image use requires a trusted signed approval, never a request boolean.', matches: c => /^(container\.|deployment\.)/.test(c.id) },
    Logs: { description: 'Query user-owned persisted events and execution logs. Use filters for session, event kind and cursor.', matches: c => c.id.startsWith('events.') || c.id === 'container.manage' },
    Stats: { description: 'Read persisted usage, message counts and tool activity for this user. Unavailable providers are reported, not replaced with fake totals.', matches: c => c.id.startsWith('stats.') },
    Tools: { description: 'Discover every advertised capability and operation. This is a scoped SDK console, not an arbitrary HTTP proxy.', matches: () => true }
  };
  async function refresh() { try { topology = await json('/api/topology'); } catch (e) { error = e.message; } }
  async function login() { try { error = ''; const data = await json('/api/auth/login', 'POST', { username, password }); user = data.user; setCSRF(data.csrf); password = ''; await refresh(); } catch (e) { error = e.message; } }
  async function logout() { try { await json('/api/auth/logout', 'POST', {}); user = null; topology = null; setCSRF(''); } catch (e) { error = e.message; } }
  async function changePassword() { try { await json('/api/auth/password', 'PUT', { current: currentPassword, password: newPassword }); currentPassword = ''; newPassword = ''; user = null; setCSRF(''); } catch (e) { error = e.message; } }
  async function loadAccounts() { try { accounts = await json('/api/users'); } catch (e) { error = e.message; } }
  async function createAccount() { try { await json('/api/users', 'PUT', { username: accountName, password: accountPassword, scopes: accountScopes.split(',').map(s => s.trim()).filter(Boolean) }); accountPassword = ''; accountName = ''; await loadAccounts(); } catch (e) { error = e.message; } }
  async function approveImage() { try { approvalResult = await json('/api/container/approval', 'POST', { image, node: executorNode }); } catch (e) { error = e.message; } }
  onMount(() => {
    let stopped = false;
    (async () => { try { const data = await json('/api/auth/me'); if (stopped) return; user = data.user; setCSRF(data.csrf); await refresh(); } catch { /* Login remains available without an existing session. */ } finally { loading = false; } })();
    const timer = setInterval(() => { if (user) refresh(); }, 10000);
    return () => { stopped = true; clearInterval(timer); };
  });
</script>
<svelte:head><title>{tab} · WhaleMesh</title></svelte:head>
{#if loading}<main class="login"><h1>WhaleMesh</h1><p>Loading user session…</p></main>
{:else if !user}
<main class="login"><section class="panel"><div class="brand">W</div><h1>WhaleMesh</h1><p>Autonomous capability mesh</p><form on:submit|preventDefault={login}><label>Username<input bind:value={username} autocomplete="username" required /></label><label>Password<input type="password" bind:value={password} autocomplete="current-password" required /></label><button>Sign in</button></form>{#if error}<p class="error" role="alert">{error}</p>{/if}<p class="hint">No default password. Retrieve the bootstrap password from your web node identity volume. Browser identity is separate from Mesh node trust.</p></section></main>
{:else}
<div class="shell"><nav aria-label="Main navigation"><a class="wordmark" href="#Nodes" on:click={() => tab = 'Nodes'}><span class="brand">W</span>WhaleMesh</a>{#each pages as page}<button class:active={tab === page} aria-current={tab === page ? 'page' : undefined} on:click={() => { tab = page; error = ''; }}>{page}</button>{/each}<div class="nav-user"><strong>{user.username}</strong><small>Browser user</small><button on:click={logout}>Sign out</button></div></nav>
<main><header><div><h1>{tab}</h1><p>{topology?.manifest?.identity?.namespace || 'Mesh'} · {topology?.peers?.length || 0} discovered peers · {providers.length} capabilities</p></div><span class="badge">{topology?.manifest?.lifecycle?.ready ? 'Node ready' : 'Connecting'}</span></header>
{#if error}<div class="error" role="alert">{error}<button on:click={() => error = ''}>Dismiss</button></div>{/if}
{#if tab === 'Nodes'}<Nodes {topology} {providers} {refresh} />
{:else if tab === 'Chat'}<Chat {providers} />
{:else if tab === 'Models'}<Models {providers} />
{:else if tab === 'Adapters'}<Adapters {providers} />
{:else if tab === 'Benchmark'}<Benchmark {providers} />
{:else if tab === 'Topology'}<Topology {topology} {refresh} />
{:else if tab === 'Account'}
<section class="panel"><h2>Current identity</h2><p>Browser user: {user.username}</p><p>User ID: <code>{user.id}</code></p><p>Granted scopes: <code>{user.scopes.join(', ')}</code></p><p>Mesh principal: <code>{topology?.manifest?.identity?.principal || 'Not available'}</code></p><p class="hint">Mesh grants limit the backend. User scopes further limit browser actions. User passwords never authenticate Mesh peers.</p></section>
<div class="columns"><section class="panel"><h3>Change password</h3><form on:submit|preventDefault={changePassword}><label>Current password<input type="password" bind:value={currentPassword} autocomplete="current-password" required /></label><label>New password (12–72 bytes)<input type="password" bind:value={newPassword} autocomplete="new-password" minlength="12" maxlength="72" required /></label><button>Change and sign out all sessions</button></form></section>
<section class="panel"><h3>Create scoped user</h3><form on:submit|preventDefault={createAccount}><label>Username<input bind:value={accountName} required autocomplete="off" /></label><label>Password<input type="password" bind:value={accountPassword} minlength="12" maxlength="72" required autocomplete="new-password" /></label><label>Scopes (comma separated)<input bind:value={accountScopes} required /></label><button>Create user</button></form><button on:click={loadAccounts}>List users</button>{#each accounts as account}<p><strong>{account.username}</strong> · {account.scopes.join(', ')}</p>{/each}</section></div>
{:else}
{#key tab}<Resource title={tab} description={resources[tab].description} providers={providers.filter(item => resources[tab].matches(item.capability))} />{/key}
{#if tab === 'Docker'}<section class="panel"><h3>Signed image approval</h3><p>Trusted administrator approval binds an immutable image digest to this user and namespace. It expires after one hour. Browser booleans cannot grant permission.</p><form on:submit|preventDefault={approveImage}><div class="row"><label>Executor<select bind:value={executorNode}><option value="">Automatically resolve</option>{#each providers.filter(item => item.capability.id === 'container.manage') as item}<option value={nodeID(item.node)}>{item.node.identity.name}</option>{/each}</select></label><label>Immutable image reference<input bind:value={image} required pattern=".+@sha256:[a-fA-F0-9]{64}" /></label><button>Sign approval</button></div></form>{#if approvalResult}<pre>{JSON.stringify(approvalResult, null, 2)}</pre>{/if}</section>{/if}
{/if}
<footer>WhaleMesh · signed peer trust · user-scoped capability calls</footer></main></div>
{/if}
