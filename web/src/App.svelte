<script>
  import { onMount } from 'svelte';

  let activeTab = $state('threads');
  let repo = $state('');
  let repoInput = $state('');
  let repositories = $state([]);
  let statusReport = $state(null);
  let stats = $state(null);

  let loading = $state(false);
  let busyOp = $state('');
  let lastNotice = $state('ready');
  let lastError = $state('');

  let syncDrawerOpen = $state(false);
  let analyzeDrawerOpen = $state(false);

  // Threads state
  let threadStatusFilter = $state('');
  let threads = $state([]);
  let selectedThreadId = $state('');
  let selectedThreadDetail = $state(null);

  // Relationships state
  let relTypeFilter = $state('');
  let relVerifiedFilter = $state('');
  let relThreadFilter = $state('');
  let relItemFilter = $state('');
  let relationships = $state([]);

  // Items state
  let itemKindFilter = $state('');
  let itemStateFilter = $state('');
  let itemDirtyFilter = $state('');
  let itemLimit = $state(100);
  let items = $state([]);
  let selectedItemNum = $state(0);
  let selectedItemDetail = $state(null);

  // Sync & Analyze parameters
  let syncLimit = $state(100);
  let syncFull = $state(false);
  let analyzeItem = $state('');
  let analyzeLimit = $state(100);
  let analyzeReanalyze = $state(false);
  let analyzeFinder = $state('');

  function fmtLine(idx) {
    return 'L' + String(idx + 1).padStart(3, '0');
  }

  function fmtTime(iso) {
    if (!iso || iso.startsWith('0001-01-01')) return '-';
    return iso.replace('T', ' ').replace(/\.\d+Z$/, 'Z');
  }

  function isItemDirty(it) {
    if (!it) return false;
    return !it.last_analyzed_hash || it.content_hash !== it.last_analyzed_hash;
  }

  function currentRepoWatermark() {
    const trimmed = repo.trim() || statusReport?.config?.repository || '';
    if (!trimmed) return null;
    return repositories.find((r) => r.repository === trimmed) || null;
  }

  function statusClass(val) {
    switch (val) {
      case 'pass':
      case 'clean':
      case 'active':
      case 'verified':
      case 'open':
      case 'merged':
      case 'answered':
      case 'high':
        return 'st-ok';
      case 'dirty':
      case 'unverified':
      case 'stale':
      case 'unconfigured':
      case 'medium':
        return 'st-warn';
      case 'fail':
      case 'error':
      case 'low':
        return 'st-err';
      default:
        return 'st-muted';
    }
  }

  async function apiFetch(path, options = {}) {
    const { headers: customHeaders, ...restOptions } = options;
    const reqHeaders = { ...(customHeaders || {}) };
    if (restOptions.body !== undefined && !reqHeaders['Content-Type']) {
      reqHeaders['Content-Type'] = 'application/json';
    }
    const res = await fetch(path, {
      ...restOptions,
      headers: reqHeaders
    });
    const rawText = await res.text();
    let body = null;
    if (rawText) {
      try {
        body = JSON.parse(rawText);
      } catch {
        throw new Error(`HTTP ${res.status}: ${rawText.slice(0, 160)}`);
      }
    }
    if (!res.ok || !body?.ok) {
      const msg = body?.error?.message || `HTTP ${res.status}`;
      const code = body?.error?.code || 'ERR';
      throw new Error(`${code}: ${msg}`);
    }
    return body.data;
  }

  async function bootstrap() {
    loading = true;
    lastError = '';
    try {
      const [st, repoData] = await Promise.all([
        apiFetch('/api/status'),
        apiFetch('/api/repositories')
      ]);
      statusReport = st;
      repositories = Array.isArray(repoData) ? repoData : [];
      let initialRepo = '';
      if (st?.config?.repository) {
        initialRepo = st.config.repository;
      } else if (repositories.length > 0) {
        initialRepo = repositories[0].repository;
      }
      if (initialRepo) {
        repoInput = initialRepo;
        await refreshActiveRepo(initialRepo, { skipGlobalStatus: true });
      } else {
        lastNotice = 'select repository';
      }
    } catch (err) {
      lastError = err.message || String(err);
    } finally {
      loading = false;
    }
  }

  async function refreshActiveRepo(targetRepo = repoInput, opts = {}) {
    const trimmed = String(targetRepo ?? '').trim();
    if (!trimmed) {
      lastError = 'ERR_INVALID_INPUT: repository is required (owner/name)';
      return;
    }
    const switchingRepo = trimmed !== repo;
    loading = true;
    lastError = '';
    try {
      const promises = [apiFetch(`/api/stats?repo=${encodeURIComponent(trimmed)}`)];
      if (!opts.skipGlobalStatus) {
        promises.push(apiFetch('/api/status'), apiFetch('/api/repositories'));
      }
      const results = await Promise.all(promises);
      stats = results[0];
      if (!opts.skipGlobalStatus) {
        statusReport = results[1];
        repositories = Array.isArray(results[2]) ? results[2] : [];
      }
      repo = trimmed;
      repoInput = trimmed;
      if (switchingRepo) {
        selectedThreadId = '';
        selectedThreadDetail = null;
        selectedItemNum = 0;
        selectedItemDetail = null;
      }
      await Promise.all([
        loadThreads({ throwOnError: true }),
        loadRelationships({ throwOnError: true }),
        loadItems({ throwOnError: true })
      ]);
      if (!opts.preserveNotice) {
        lastNotice = `loaded ${trimmed}`;
      }
    } catch (err) {
      lastError = err.message || String(err);
    } finally {
      loading = false;
    }
  }

  async function loadThreads(opts = {}) {
    const trimmed = repo.trim();
    if (!trimmed) return;
    if (!opts.throwOnError) lastError = '';
    try {
      const params = new URLSearchParams({ repo: trimmed });
      if (threadStatusFilter) params.set('status', threadStatusFilter);
      const data = await apiFetch(`/api/threads?${params.toString()}`);
      threads = Array.isArray(data) ? data : [];
      if (
        selectedThreadId &&
        (threads.some((t) => t.id === selectedThreadId) ||
          selectedThreadDetail?.thread?.id === selectedThreadId)
      ) {
        await selectThread(selectedThreadId, opts);
      } else if (threads.length > 0) {
        await selectThread(threads[0].id, opts);
      } else {
        selectedThreadId = '';
        selectedThreadDetail = null;
      }
    } catch (err) {
      lastError = err.message || String(err);
      if (opts.throwOnError) throw err;
    }
  }

  async function selectThread(id, opts = {}) {
    if (!id) return;
    if (selectedThreadDetail?.thread?.id !== id) {
      selectedThreadDetail = null;
    }
    selectedThreadId = id;
    try {
      const params = new URLSearchParams({ id });
      const detail = await apiFetch(`/api/threads/show?${params.toString()}`);
      if (selectedThreadId === id) {
        selectedThreadDetail = detail;
      }
    } catch (err) {
      if (selectedThreadId === id) {
        selectedThreadDetail = null;
      }
      lastError = err.message || String(err);
      if (opts.throwOnError) throw err;
    }
  }

  async function loadRelationships(opts = {}) {
    const trimmed = repo.trim();
    if (!trimmed) return;
    if (!opts.throwOnError) lastError = '';
    try {
      const params = new URLSearchParams({ repo: trimmed });
      if (relTypeFilter) params.set('type', relTypeFilter);
      if (relVerifiedFilter) params.set('verified', relVerifiedFilter);
      if (relThreadFilter.trim()) params.set('thread', relThreadFilter.trim());
      const rawItem = String(relItemFilter ?? '').trim();
      if (rawItem !== '') {
        params.set('item', rawItem);
      }
      const data = await apiFetch(`/api/relationships?${params.toString()}`);
      relationships = Array.isArray(data) ? data : [];
    } catch (err) {
      lastError = err.message || String(err);
      if (opts.throwOnError) throw err;
    }
  }

  async function loadItems(opts = {}) {
    const trimmed = repo.trim();
    if (!trimmed) return;
    if (!opts.throwOnError) lastError = '';
    try {
      const limitVal = Number(itemLimit);
      if (!Number.isInteger(limitVal) || limitVal < 1) {
        throw new Error('ERR_INVALID_INPUT: item limit must be a positive integer');
      }
      const params = new URLSearchParams({ repo: trimmed, limit: String(limitVal) });
      if (itemKindFilter) params.set('kind', itemKindFilter);
      if (itemStateFilter) params.set('state', itemStateFilter);
      if (itemDirtyFilter === 'true' || itemDirtyFilter === 'false') {
        params.set('dirty', itemDirtyFilter);
      }
      const data = await apiFetch(`/api/items?${params.toString()}`);
      items = Array.isArray(data) ? data : [];
      if (
        selectedItemNum &&
        (items.some((it) => it.number === selectedItemNum) ||
          selectedItemDetail?.item?.number === selectedItemNum)
      ) {
        await selectItem(selectedItemNum, opts);
      } else if (items.length > 0) {
        await selectItem(items[0].number, opts);
      } else {
        selectedItemNum = 0;
        selectedItemDetail = null;
      }
    } catch (err) {
      lastError = err.message || String(err);
      if (opts.throwOnError) throw err;
    }
  }

  async function selectItem(number, opts = {}) {
    const trimmed = repo.trim();
    if (!trimmed || !number) return;
    if (selectedItemDetail?.item?.number !== number) {
      selectedItemDetail = null;
    }
    selectedItemNum = number;
    try {
      const detail = await apiFetch(
        `/api/items/${encodeURIComponent(String(number))}?repo=${encodeURIComponent(trimmed)}`
      );
      if (selectedItemNum === number) {
        selectedItemDetail = detail;
      }
    } catch (err) {
      if (selectedItemNum === number) {
        selectedItemDetail = null;
      }
      lastError = err.message || String(err);
      if (opts.throwOnError) throw err;
    }
  }

  async function toggleVerifyRelationship(rel) {
    if (!rel?.id) return;
    busyOp = `verify:${rel.id}`;
    lastError = '';
    try {
      const nextVerified = !rel.verified_by_human;
      await apiFetch('/api/relationships/verify', {
        method: 'POST',
        body: JSON.stringify({
          id: rel.id,
          verified: nextVerified
        })
      });
      lastNotice = `${rel.id} -> ${nextVerified ? 'verified' : 'unverified'}`;
      const trimmed = repo.trim();
      const [statsData] = await Promise.all([
        trimmed ? apiFetch(`/api/stats?repo=${encodeURIComponent(trimmed)}`) : Promise.resolve(stats),
        loadRelationships({ throwOnError: true }),
        selectedThreadId ? selectThread(selectedThreadId, { throwOnError: true }) : Promise.resolve()
      ]);
      stats = statsData;
    } catch (err) {
      lastError = err.message || String(err);
    } finally {
      busyOp = '';
    }
  }

  async function triggerSync(e) {
    if (e) e.preventDefault();
    const targetRepo = repoInput.trim() || repo.trim();
    if (!targetRepo) {
      lastError = 'ERR_INVALID_INPUT: repository is required for sync';
      return;
    }
    syncDrawerOpen = false;
    busyOp = 'sync';
    lastError = '';
    try {
      const res = await apiFetch('/api/sync', {
        method: 'POST',
        body: JSON.stringify({
          repository: targetRepo,
          limit: Number(syncLimit) || 100,
          full: Boolean(syncFull)
        })
      });
      lastNotice = `sync pass: ${res.fetched_items} items (${res.dirty_items_pending_analysis} dirty), ${res.fetched_comments} comments`;
      await refreshActiveRepo(targetRepo, { preserveNotice: true });
    } catch (err) {
      lastError = err.message || String(err);
    } finally {
      busyOp = '';
    }
  }

  async function triggerAnalyze(e, specificItemNumber = 0) {
    if (e) e.preventDefault();
    const targetRepo = specificItemNumber > 0 ? repo.trim() : repoInput.trim() || repo.trim();
    if (!targetRepo) {
      lastError = 'ERR_INVALID_INPUT: repository is required for analyze';
      return;
    }
    analyzeDrawerOpen = false;
    busyOp = 'analyze';
    lastError = '';
    const rawItemStr = String(analyzeItem ?? '').trim();
    const payload = {
      repository: targetRepo,
      limit: Number(analyzeLimit) || 100,
      reanalyze: specificItemNumber > 0 ? true : Boolean(analyzeReanalyze)
    };
    if (specificItemNumber > 0) {
      payload.item_number = specificItemNumber;
    } else if (rawItemStr !== '') {
      payload.item_number = Number(rawItemStr);
    }
    if (analyzeFinder) {
      payload.finder = analyzeFinder;
    }
    try {
      const res = await apiFetch('/api/analyze', {
        method: 'POST',
        body: JSON.stringify(payload)
      });
      lastNotice = `analyze pass: ${res.analyzed_items} items, ${res.threads_created} new threads, ${res.threads_updated} updated, ${res.relationships_upserted} rels`;
      await refreshActiveRepo(targetRepo, { preserveNotice: true });
    } catch (err) {
      lastError = err.message || String(err);
    } finally {
      busyOp = '';
    }
  }

  function inspectItemFromThread(num) {
    activeTab = 'items';
    selectItem(num);
  }

  onMount(() => {
    bootstrap();
  });
</script>

<div class="shell">
  <header class="topbar">
    <div class="topbar-left">
      <span class="brand">AUTOMAIN</span>

      <form
        class="inline-form"
        onsubmit={(e) => {
          e.preventDefault();
          refreshActiveRepo(repoInput);
        }}
      >
        <label for="repo-input">repo</label>
        <input
          id="repo-input"
          type="text"
          list="repo-datalist"
          placeholder="owner/name"
          bind:value={repoInput}
          size="22"
        />
        <datalist id="repo-datalist">
          {#each repositories as r}
            <option value={r.repository}></option>
          {/each}
        </datalist>
        <button type="submit" disabled={loading || Boolean(busyOp)}>load</button>
      </form>

      <details class="action-drawer" bind:open={syncDrawerOpen}>
        <summary>sync</summary>
        <form class="drawer-panel" onsubmit={triggerSync}>
          <label for="sync-limit">limit</label>
          <input id="sync-limit" type="number" min="1" max="5000" bind:value={syncLimit} style="width:64px" />
          <label>
            <input type="checkbox" bind:checked={syncFull} />
            full
          </label>
          <button type="submit" disabled={busyOp === 'sync' || !(repoInput.trim() || repo.trim())}>
            {busyOp === 'sync' ? 'running' : 'run sync'}
          </button>
        </form>
      </details>

      <details class="action-drawer" bind:open={analyzeDrawerOpen}>
        <summary>analyze</summary>
        <form class="drawer-panel" onsubmit={(e) => triggerAnalyze(e, 0)}>
          <label for="analyze-item">item#</label>
          <input
            id="analyze-item"
            type="number"
            min="1"
            placeholder="all"
            bind:value={analyzeItem}
            style="width:64px"
          />
          <label for="analyze-limit">limit</label>
          <input
            id="analyze-limit"
            type="number"
            min="1"
            max="5000"
            bind:value={analyzeLimit}
            style="width:64px"
          />
          <label for="analyze-finder">finder</label>
          <select id="analyze-finder" bind:value={analyzeFinder}>
            <option value="">default ({statusReport?.config?.finder || 'llm'})</option>
            <option value="llm">llm</option>
            <option value="fts">fts</option>
            <option value="vector">vector</option>
          </select>
          <label>
            <input type="checkbox" bind:checked={analyzeReanalyze} />
            reanalyze
          </label>
          <button type="submit" disabled={busyOp === 'analyze' || !(repoInput.trim() || repo.trim())}>
            {busyOp === 'analyze' ? 'running' : 'run analyze'}
          </button>
        </form>
      </details>
    </div>

    <div class="topbar-right mono">
      <span>v:{statusReport?.version || '-'}</span>
      <span>finder:{statusReport?.config?.finder || '-'}</span>
      <span>
        db:<span class={statusClass(statusReport?.adapters?.storage ? 'pass' : 'fail')}>
          {statusReport?.adapters?.storage ? 'pass' : 'fail'}
        </span>
      </span>
      <span>
        sync:<span class={statusClass(statusReport?.adapters?.github ? 'pass' : 'unconfigured')}>
          {statusReport?.adapters?.github ? 'pass' : 'unconfigured'}
        </span>
      </span>
      <span>
        analysis:<span class={statusClass(statusReport?.adapters?.relationship_analyser ? 'pass' : 'unconfigured')}>
          {statusReport?.adapters?.relationship_analyser ? 'pass' : 'unconfigured'}
        </span>
      </span>
    </div>
  </header>

  <section class="metrics-strip">
    <div class="metric-item">
      <span class="metric-key">repo:</span>
      <span class="metric-val">{stats?.repository || repo || '-'}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">items:</span>
      <span class="metric-val">{stats?.total_items ?? 0}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">dirty:</span>
      <span class="metric-val {stats?.dirty_items_count > 0 ? 'st-warn' : 'st-ok'}">
        {stats?.dirty_items_count ?? 0}
      </span>
    </div>
    <div class="metric-item">
      <span class="metric-key">threads:</span>
      <span class="metric-val">{stats?.total_threads ?? 0}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">relationships:</span>
      <span class="metric-val">{stats?.total_relationships ?? 0}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">verified:</span>
      <span class="metric-val st-ok">{stats?.verified_relationships_count ?? 0}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">non_obvious:</span>
      <span class="metric-val">{stats?.non_obvious_relationships_count ?? 0}</span>
    </div>
    <div class="metric-item">
      <span class="metric-key">multi_thread:</span>
      <span class="metric-val">{stats?.multi_thread_items_count ?? 0}</span>
    </div>
  </section>

  <div class="status-line">
    <div>
      <span class="st-muted">status:</span>
      {#if lastError}
        <span class="st-err">{lastError}</span>
      {:else if loading || busyOp}
        <span class="st-warn">{busyOp || 'loading'}</span>
      {:else}
        <span class="st-ok">{lastNotice}</span>
      {/if}
    </div>
    <div class="st-muted">
      last_sync: {fmtTime(currentRepoWatermark()?.last_synced_at)}
    </div>
  </div>

  <nav class="nav-bar">
    <div class="nav-tabs">
      <button
        type="button"
        class={activeTab === 'threads' ? 'active-tab' : ''}
        onclick={() => (activeTab = 'threads')}
      >
        threads ({threads.length})
      </button>
      <button
        type="button"
        class={activeTab === 'relationships' ? 'active-tab' : ''}
        onclick={() => (activeTab = 'relationships')}
      >
        relationships ({relationships.length})
      </button>
      <button
        type="button"
        class={activeTab === 'items' ? 'active-tab' : ''}
        onclick={() => (activeTab = 'items')}
      >
        items ({items.length})
      </button>
      <button
        type="button"
        class={activeTab === 'status' ? 'active-tab' : ''}
        onclick={() => (activeTab = 'status')}
      >
        status ({repositories.length})
      </button>
    </div>

    {#if activeTab === 'threads'}
      <form
        class="filter-bar"
        onsubmit={(e) => {
          e.preventDefault();
          loadThreads();
        }}
      >
        <label for="th-status">status</label>
        <select id="th-status" bind:value={threadStatusFilter} onchange={() => loadThreads()}>
          <option value="">all</option>
          <option value="active">active</option>
          <option value="resolved">resolved</option>
          <option value="stale">stale</option>
        </select>
        <button type="submit">apply</button>
      </form>
    {:else if activeTab === 'relationships'}
      <form
        class="filter-bar"
        onsubmit={(e) => {
          e.preventDefault();
          loadRelationships();
        }}
      >
        <label for="rel-type">type</label>
        <select id="rel-type" bind:value={relTypeFilter} onchange={() => loadRelationships()}>
          <option value="">all</option>
          <option value="reports_same_problem">reports_same_problem</option>
          <option value="proposes_fix">proposes_fix</option>
          <option value="implements_decision">implements_decision</option>
          <option value="blocks_or_depends">blocks_or_depends</option>
          <option value="related_context">related_context</option>
          <option value="duplicate_of">duplicate_of</option>
        </select>
        <label for="rel-ver">verified</label>
        <select id="rel-ver" bind:value={relVerifiedFilter} onchange={() => loadRelationships()}>
          <option value="">all</option>
          <option value="true">verified</option>
          <option value="false">unverified</option>
        </select>
        <label for="rel-thread">thread</label>
        <input id="rel-thread" type="text" placeholder="th_..." bind:value={relThreadFilter} size="12" />
        <label for="rel-item">item#</label>
        <input id="rel-item" type="number" min="1" placeholder="any" bind:value={relItemFilter} style="width:58px" />
        <button type="submit">apply</button>
      </form>
    {:else if activeTab === 'items'}
      <form
        class="filter-bar"
        onsubmit={(e) => {
          e.preventDefault();
          loadItems();
        }}
      >
        <label for="it-kind">kind</label>
        <select id="it-kind" bind:value={itemKindFilter} onchange={() => loadItems()}>
          <option value="">all</option>
          <option value="issue">issue</option>
          <option value="pull_request">pull_request</option>
          <option value="discussion">discussion</option>
        </select>
        <label for="it-state">state</label>
        <select id="it-state" bind:value={itemStateFilter} onchange={() => loadItems()}>
          <option value="">all</option>
          <option value="open">open</option>
          <option value="closed">closed</option>
          <option value="merged">merged</option>
          <option value="answered">answered</option>
        </select>
        <label for="it-dirty">sync</label>
        <select id="it-dirty" bind:value={itemDirtyFilter} onchange={() => loadItems()}>
          <option value="">all</option>
          <option value="true">dirty</option>
          <option value="false">clean</option>
        </select>
        <label for="it-limit">limit</label>
        <input id="it-limit" type="number" min="1" max="1000" bind:value={itemLimit} style="width:58px" />
        <button type="submit">apply</button>
      </form>
    {/if}
  </nav>

  {#if activeTab === 'threads'}
    <div class="workspace-split">
      <div class="pane">
        <div class="pane-header">
          <span>THREADS</span>
          <span class="st-muted">rows: {threads.length}</span>
        </div>
        <table class="data-table">
          <thead>
            <tr>
              <th>line</th>
              <th>id</th>
              <th>status</th>
              <th class="num">items</th>
              <th class="num">rev</th>
              <th>title / summary</th>
              <th>updated</th>
            </tr>
          </thead>
          <tbody>
            {#if threads.length === 0}
              <tr>
                <td class="lineno">L000</td>
                <td colspan="6" class="st-muted">0 threads found</td>
              </tr>
            {:else}
              {#each threads as th, idx}
                <tr
                  class="clickable {selectedThreadId === th.id ? 'selected' : ''}"
                  onclick={() => selectThread(th.id)}
                >
                  <td class="lineno">{fmtLine(idx)}</td>
                  <td class="mono nowrap">{th.id}</td>
                  <td class={statusClass(th.status)}>{th.status}</td>
                  <td class="num">{th.item_count}</td>
                  <td class="num">r{th.revision}</td>
                  <td>
                    <div>{th.title}</div>
                    {#if th.summary}
                      <div class="st-muted mono">{th.summary}</div>
                    {/if}
                  </td>
                  <td class="mono nowrap st-muted">{fmtTime(th.updated_at)}</td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>

      <div class="pane">
        <div class="pane-header">
          <span>THREAD DETAIL {selectedThreadDetail?.thread?.id || ''}</span>
          {#if selectedThreadDetail?.thread}
            <span class={statusClass(selectedThreadDetail.thread.status)}>
              {selectedThreadDetail.thread.status}
            </span>
          {/if}
        </div>

        {#if !selectedThreadDetail?.thread}
          <table class="data-table">
            <tbody>
              <tr>
                <td class="lineno">L000</td>
                <td class="st-muted">select a thread row to inspect members and evidence</td>
              </tr>
            </tbody>
          </table>
        {:else}
          <div class="section-block">
            <table class="data-table">
              <tbody>
                <tr>
                  <td class="lineno">L001</td>
                  <td class="mono st-muted">id</td>
                  <td class="mono">{selectedThreadDetail.thread.id}</td>
                  <td class="mono st-muted">revision</td>
                  <td class="mono">r{selectedThreadDetail.thread.revision}</td>
                </tr>
                <tr>
                  <td class="lineno">L002</td>
                  <td class="mono st-muted">title</td>
                  <td colspan="3">{selectedThreadDetail.thread.title}</td>
                </tr>
                <tr>
                  <td class="lineno">L003</td>
                  <td class="mono st-muted">summary</td>
                  <td colspan="3" class="mono">{selectedThreadDetail.thread.summary || '-'}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="pane-header">
            <span>MEMBER ITEMS</span>
            <span class="st-muted">count: {selectedThreadDetail.items?.length || 0}</span>
          </div>
          <div class="section-block">
            <table class="data-table">
              <thead>
                <tr>
                  <th>line</th>
                  <th>item</th>
                  <th>kind</th>
                  <th>state</th>
                  <th>author</th>
                  <th>title</th>
                  <th>action</th>
                </tr>
              </thead>
              <tbody>
                {#each selectedThreadDetail.items || [] as entry, idx}
                  <tr>
                    <td class="lineno">{fmtLine(idx)}</td>
                    <td class="mono nowrap">#{entry.number}</td>
                    <td class="mono">{entry.kind}</td>
                    <td class={statusClass(entry.state)}>{entry.state}</td>
                    <td class="mono">{entry.author || '-'}</td>
                    <td>{entry.title}</td>
                    <td class="nowrap">
                      <button type="button" onclick={() => inspectItemFromThread(entry.number)}>
                        view
                      </button>
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          </div>

          <div class="pane-header">
            <span>THREAD RELATIONSHIPS</span>
            <span class="st-muted">count: {selectedThreadDetail.relationships?.length || 0}</span>
          </div>
          <table class="data-table">
            <thead>
              <tr>
                <th>line</th>
                <th>id</th>
                <th>item</th>
                <th>type</th>
                <th>confidence</th>
                <th>status</th>
                <th>evidence</th>
                <th>action</th>
              </tr>
            </thead>
            <tbody>
              {#if (selectedThreadDetail.relationships || []).length === 0}
                <tr>
                  <td class="lineno">L000</td>
                  <td colspan="7" class="st-muted">0 relationships in thread</td>
                </tr>
              {:else}
                {#each selectedThreadDetail.relationships || [] as rel, idx}
                  <tr>
                    <td class="lineno">{fmtLine(idx)}</td>
                    <td class="mono nowrap">{rel.id}</td>
                    <td class="mono nowrap">#{rel.item_number}</td>
                    <td class="mono">{rel.type}</td>
                    <td class={statusClass(rel.confidence)}>{rel.confidence}</td>
                    <td class={statusClass(rel.verified_by_human ? 'verified' : 'unverified')}>
                      {rel.verified_by_human ? 'verified' : 'unverified'}
                    </td>
                    <td>
                      <details class="inline-details">
                        <summary>{rel.evidence?.length || 0} hits</summary>
                        <div class="mono st-muted">{rel.explanation}</div>
                        <table class="data-table">
                          <tbody>
                            {#each rel.evidence || [] as ev, evIdx}
                              <tr>
                                <td class="lineno">{fmtLine(evIdx)}</td>
                                <td class="mono nowrap">
                                  {ev.source_kind}:#{ev.item_number}{ev.comment_id ? `:${ev.comment_id}` : ''}
                                </td>
                                <td class="mono nowrap">{ev.author || '-'}</td>
                                <td class="mono">{ev.excerpt}</td>
                              </tr>
                            {/each}
                          </tbody>
                        </table>
                      </details>
                    </td>
                    <td>
                      <button
                        type="button"
                        disabled={busyOp === `verify:${rel.id}`}
                        onclick={() => toggleVerifyRelationship(rel)}
                      >
                        {rel.verified_by_human ? 'unverify' : 'verify'}
                      </button>
                    </td>
                  </tr>
                {/each}
              {/if}
            </tbody>
          </table>
        {/if}
      </div>
    </div>
  {:else if activeTab === 'relationships'}
    <div class="pane" style="flex:1">
      <div class="pane-header">
        <span>RELATIONSHIPS</span>
        <span class="st-muted">rows: {relationships.length}</span>
      </div>
      <table class="data-table">
        <thead>
          <tr>
            <th>line</th>
            <th>id</th>
            <th>item</th>
            <th>thread</th>
            <th>type</th>
            <th>confidence</th>
            <th>link</th>
            <th>status</th>
            <th>explanation</th>
            <th>evidence</th>
            <th>action</th>
          </tr>
        </thead>
        <tbody>
          {#if relationships.length === 0}
            <tr>
              <td class="lineno">L000</td>
              <td colspan="10" class="st-muted">0 relationships matching filter</td>
            </tr>
          {:else}
            {#each relationships as rel, idx}
              <tr>
                <td class="lineno">{fmtLine(idx)}</td>
                <td class="mono nowrap">{rel.id}</td>
                <td class="mono nowrap">#{rel.item_number}</td>
                <td class="mono nowrap">
                  {#if rel.thread_id}
                    <a
                      href="#thread"
                      onclick={(e) => {
                        e.preventDefault();
                        activeTab = 'threads';
                        selectThread(rel.thread_id);
                      }}
                    >
                      {rel.thread_id}
                    </a>
                  {:else}
                    -
                  {/if}
                </td>
                <td class="mono">{rel.type}</td>
                <td class={statusClass(rel.confidence)}>{rel.confidence}</td>
                <td class="mono">{rel.explicitly_linked ? 'explicit' : 'non_obvious'}</td>
                <td class={statusClass(rel.verified_by_human ? 'verified' : 'unverified')}>
                  {rel.verified_by_human ? 'verified' : 'unverified'}
                </td>
                <td>{rel.explanation}</td>
                <td>
                  <details class="inline-details">
                    <summary>{rel.evidence?.length || 0} hits</summary>
                    <table class="data-table">
                      <thead>
                        <tr>
                          <th>line</th>
                          <th>source</th>
                          <th>author</th>
                          <th>excerpt</th>
                        </tr>
                      </thead>
                      <tbody>
                        {#each rel.evidence || [] as ev, evIdx}
                          <tr>
                            <td class="lineno">{fmtLine(evIdx)}</td>
                            <td class="mono nowrap">
                              {ev.source_kind}:#{ev.item_number}{ev.comment_id ? `:${ev.comment_id}` : ''}
                            </td>
                            <td class="mono nowrap">{ev.author || '-'}</td>
                            <td class="mono">{ev.excerpt}</td>
                          </tr>
                        {/each}
                      </tbody>
                    </table>
                  </details>
                </td>
                <td>
                  <button
                    type="button"
                    disabled={busyOp === `verify:${rel.id}`}
                    onclick={() => toggleVerifyRelationship(rel)}
                  >
                    {rel.verified_by_human ? 'unverify' : 'verify'}
                  </button>
                </td>
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>
  {:else if activeTab === 'items'}
    <div class="workspace-split">
      <div class="pane">
        <div class="pane-header">
          <span>REPOSITORY ITEMS</span>
          <span class="st-muted">rows: {items.length}</span>
        </div>
        <table class="data-table">
          <thead>
            <tr>
              <th>line</th>
              <th class="num">num</th>
              <th>kind</th>
              <th>state</th>
              <th>sync</th>
              <th>author</th>
              <th class="num">labels</th>
              <th>title</th>
              <th>updated</th>
            </tr>
          </thead>
          <tbody>
            {#if items.length === 0}
              <tr>
                <td class="lineno">L000</td>
                <td colspan="8" class="st-muted">0 items matching filter</td>
              </tr>
            {:else}
              {#each items as it, idx}
                {@const dirty = isItemDirty(it)}
                <tr
                  class="clickable {selectedItemNum === it.number ? 'selected' : ''}"
                  onclick={() => selectItem(it.number)}
                >
                  <td class="lineno">{fmtLine(idx)}</td>
                  <td class="num">#{it.number}</td>
                  <td class="mono">{it.kind}</td>
                  <td class={statusClass(it.state)}>{it.state}</td>
                  <td class={statusClass(dirty ? 'dirty' : 'clean')}>
                    {dirty ? 'dirty' : 'clean'}
                  </td>
                  <td class="mono">{it.author || '-'}</td>
                  <td class="num">{(it.labels || []).length}</td>
                  <td>{it.title}</td>
                  <td class="mono nowrap st-muted">{fmtTime(it.updated_at)}</td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>

      <div class="pane">
        <div class="pane-header">
          <span>ITEM DETAIL {selectedItemDetail?.item ? `#${selectedItemDetail.item.number}` : ''}</span>
          {#if selectedItemDetail?.item}
            <button
              type="button"
              disabled={busyOp === 'analyze'}
              onclick={(e) => triggerAnalyze(e, selectedItemDetail.item.number)}
            >
              analyze #{selectedItemDetail.item.number}
            </button>
          {/if}
        </div>

        {#if !selectedItemDetail?.item}
          <table class="data-table">
            <tbody>
              <tr>
                <td class="lineno">L000</td>
                <td class="st-muted">select an item row to inspect body and comments</td>
              </tr>
            </tbody>
          </table>
        {:else}
          {@const detailDirty = isItemDirty(selectedItemDetail.item)}
          <div class="section-block">
            <table class="data-table">
              <tbody>
                <tr>
                  <td class="lineno">L001</td>
                  <td class="mono st-muted">number</td>
                  <td class="mono">#{selectedItemDetail.item.number}</td>
                  <td class="mono st-muted">kind</td>
                  <td class="mono">{selectedItemDetail.item.kind}</td>
                </tr>
                <tr>
                  <td class="lineno">L002</td>
                  <td class="mono st-muted">state</td>
                  <td class={statusClass(selectedItemDetail.item.state)}>
                    {selectedItemDetail.item.state}
                  </td>
                  <td class="mono st-muted">sync</td>
                  <td class={statusClass(detailDirty ? 'dirty' : 'clean')}>
                    {detailDirty ? 'dirty' : 'clean'}
                  </td>
                </tr>
                <tr>
                  <td class="lineno">L003</td>
                  <td class="mono st-muted">author</td>
                  <td class="mono">{selectedItemDetail.item.author || '-'}</td>
                  <td class="mono st-muted">hash</td>
                  <td class="mono">
                    {selectedItemDetail.item.content_hash
                      ? selectedItemDetail.item.content_hash.slice(0, 16)
                      : '-'}
                  </td>
                </tr>
                <tr>
                  <td class="lineno">L004</td>
                  <td class="mono st-muted">labels</td>
                  <td colspan="3" class="mono">
                    {(selectedItemDetail.item.labels || []).join(', ') || '-'}
                  </td>
                </tr>
                <tr>
                  <td class="lineno">L005</td>
                  <td class="mono st-muted">title</td>
                  <td colspan="3">{selectedItemDetail.item.title}</td>
                </tr>
              </tbody>
            </table>
          </div>

          <div class="pane-header">
            <span>BODY</span>
            <span class="st-muted">bytes: {(selectedItemDetail.item.body || '').length}</span>
          </div>
          <div class="section-block">
            <pre class="code-block">{selectedItemDetail.item.body || '(empty)'}</pre>
          </div>

          <div class="pane-header">
            <span>COMMENTS</span>
            <span class="st-muted">count: {selectedItemDetail.comments?.length || 0}</span>
          </div>
          <table class="data-table">
            <thead>
              <tr>
                <th>line</th>
                <th>id</th>
                <th>author</th>
                <th>created</th>
                <th>body</th>
              </tr>
            </thead>
            <tbody>
              {#if (selectedItemDetail.comments || []).length === 0}
                <tr>
                  <td class="lineno">L000</td>
                  <td colspan="4" class="st-muted">0 comments</td>
                </tr>
              {:else}
                {#each selectedItemDetail.comments || [] as cm, idx}
                  <tr>
                    <td class="lineno">{fmtLine(idx)}</td>
                    <td class="mono nowrap">{cm.comment_id}</td>
                    <td class="mono nowrap">{cm.author || '-'}</td>
                    <td class="mono nowrap st-muted">{fmtTime(cm.created_at)}</td>
                    <td class="mono">{cm.body}</td>
                  </tr>
                {/each}
              {/if}
            </tbody>
          </table>
        {/if}
      </div>
    </div>
  {:else if activeTab === 'status'}
    <div class="workspace-split">
      <div class="pane">
        <div class="pane-header">
          <span>SYSTEM ADAPTER STATUS</span>
          <span class="st-muted">runtime</span>
        </div>
        <table class="data-table">
          <thead>
            <tr>
              <th>line</th>
              <th>parameter</th>
              <th>value</th>
              <th>status</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td class="lineno">L001</td>
              <td class="mono">version</td>
              <td class="mono">{statusReport?.version || '-'} (schema v{statusReport?.schema_version ?? 1})</td>
              <td class="st-ok">pass</td>
            </tr>
            <tr>
              <td class="lineno">L002</td>
              <td class="mono">repository</td>
              <td class="mono">{statusReport?.config?.repository || repo || '-'}</td>
              <td class={statusClass(statusReport?.config?.repository || repo ? 'pass' : 'unconfigured')}>
                {statusReport?.config?.repository || repo ? 'pass' : 'unconfigured'}
              </td>
            </tr>
            <tr>
              <td class="lineno">L003</td>
              <td class="mono">db_path</td>
              <td class="mono">{statusReport?.config?.db_path || '-'}</td>
              <td class={statusClass(statusReport?.adapters?.storage ? 'pass' : 'fail')}>
                {statusReport?.adapters?.storage ? 'pass' : 'fail'}
              </td>
            </tr>
            <tr>
              <td class="lineno">L004</td>
              <td class="mono">listen_addr</td>
              <td class="mono">{statusReport?.config?.listen_addr || '-'}</td>
              <td class="st-ok">pass</td>
            </tr>
            <tr>
              <td class="lineno">L005</td>
              <td class="mono">finder</td>
              <td class="mono">{statusReport?.config?.finder || '-'}</td>
              <td class={statusClass(statusReport?.adapters?.candidate_finder ? 'pass' : 'unconfigured')}>
                {statusReport?.adapters?.candidate_finder ? 'pass' : 'unconfigured'}
              </td>
            </tr>
            <tr>
              <td class="lineno">L006</td>
              <td class="mono">github_sync</td>
              <td class="mono">token_set={statusReport?.config?.github_token_set ? 'true' : 'false'}</td>
              <td class={statusClass(statusReport?.adapters?.github ? 'pass' : 'unconfigured')}>
                {statusReport?.adapters?.github ? 'pass' : 'unconfigured'}
              </td>
            </tr>
            <tr>
              <td class="lineno">L007</td>
              <td class="mono">relationship_analyser</td>
              <td class="mono">model={statusReport?.config?.llm_model || '-'}</td>
              <td class={statusClass(statusReport?.adapters?.relationship_analyser ? 'pass' : 'unconfigured')}>
                {statusReport?.adapters?.relationship_analyser ? 'pass' : 'unconfigured'}
              </td>
            </tr>
            <tr>
              <td class="lineno">L008</td>
              <td class="mono">embedded_ui</td>
              <td class="mono">SPA + REST API</td>
              <td class={statusClass(statusReport?.adapters?.embedded_ui ? 'pass' : 'fail')}>
                {statusReport?.adapters?.embedded_ui ? 'pass' : 'fail'}
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="pane">
        <div class="pane-header">
          <span>TRACKED REPOSITORIES</span>
          <span class="st-muted">count: {repositories.length}</span>
        </div>
        <table class="data-table">
          <thead>
            <tr>
              <th>line</th>
              <th>repository</th>
              <th class="num">items</th>
              <th class="num">comments</th>
              <th>cursor</th>
              <th>last_synced_at</th>
              <th>action</th>
            </tr>
          </thead>
          <tbody>
            {#if repositories.length === 0}
              <tr>
                <td class="lineno">L000</td>
                <td colspan="6" class="st-muted">0 repositories in local store</td>
              </tr>
            {:else}
              {#each repositories as r, idx}
                <tr>
                  <td class="lineno">{fmtLine(idx)}</td>
                  <td class="mono">{r.repository}</td>
                  <td class="num">{r.item_count}</td>
                  <td class="num">{r.comment_count}</td>
                  <td class="mono st-muted">{r.cursor || '-'}</td>
                  <td class="mono nowrap st-muted">{fmtTime(r.last_synced_at)}</td>
                  <td>
                    <button
                      type="button"
                      onclick={() => {
                        refreshActiveRepo(r.repository);
                      }}
                    >
                      select
                    </button>
                  </td>
                </tr>
              {/each}
            {/if}
          </tbody>
        </table>
      </div>
    </div>
  {/if}
</div>
