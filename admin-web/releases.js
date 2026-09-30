'use strict';
const ReleaseUI = (() => {
  const pending = new Set();
  const uncertain = new Set();
  const checking = new Set();
  function canRegister(run, allowed) {
    return Boolean(allowed && run.can_register && run.run_attempt > 0 && run.registration?.status === 'unregistered');
  }
  function selectHost(groups, previous) {
    if (previous) return groups.some(g => g.host_id === previous) ? previous : '';
    const usable = groups.filter(g => g.status !== 'blocked');
    return usable.length === 1 ? usable[0].host_id : '';
  }
  function state(hash) {
    const query = new URLSearchParams(hash.replace(/^#release\??/, ''));
    return {tab: ['builds', 'versions', 'upgrade'].includes(query.get('tab')) ? query.get('tab') : 'builds', version: query.get('version') || '', host: query.get('host') || '', run: query.get('run') || ''};
  }
  function locationFor(value) { return '#release?' + new URLSearchParams(value).toString(); }
  function el(tag, text, cls) { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (cls) node.className = cls; return node; }
  function button(title, action, disabled = false) { const b = el('button', title); b.type = 'button'; b.disabled = disabled; b.onclick = action; return b; }
  function card(title) { const c = el('article', undefined, 'release-card'); c.append(el('h3', title)); return c; }
  async function render(ctx) {
    const {root, api, allowed, notice, showResult, go, isCurrent, page} = ctx;
    let selected = state(location.hash);
    const reload = () => ctx.refresh();
    function navigate(changes) { selected = {...selected, ...changes}; history.replaceState(null, '', locationFor(selected)); go('builds'); }
    async function action(key, path, body) {
      if (pending.has(key) || uncertain.has(key)) return;
      pending.add(key);
      // Disable all relevant actions before a confirmation dialog can suspend us.
      root.querySelectorAll('button[data-mutation]').forEach(b => { b.disabled = true; });
      try { const result = await api(path, 'POST', body); showResult(result); }
      catch (e) { if (!e.status || e.status >= 500) uncertain.add(key); notice(e.message + (uncertain.has(key) ? ' Check persisted status before retrying.' : '')); }
      finally { pending.delete(key); if (isCurrent()) await reload(); }
    }
    if (page === 'release-settings') {
      const settings = await api('release-settings'); if (!isCurrent()) return;
      uncertain.delete('key'); root.replaceChildren(); root.append(button('Return to selected build', () => go('builds')));
      const connection = card('Build connection');
      connection.append(el('p', settings.github_configured ? `${settings.github.repository} / ${settings.github.workflow}` : 'GitHub is not configured.'));
      if(settings.github_configured) connection.append(el('p', settings.github_connected ? 'Provider read succeeded.' : settings.github_error || 'Provider status unavailable.'));
      connection.append(el('p', 'Registry credentials remain on each agent. This service does not display tokens or secret-file paths.'));
      if (settings.github) connection.append(el('p', settings.github.auto_register ? 'Automatic trusted registration enabled; publications still require independent approval.' : 'Automatic trusted registration disabled. Enable auto_register explicitly in local GitHub configuration.'));
      if (settings.github) connection.append(el('p', settings.github.publish_images ? 'Signed image publication is enabled for approved dispatches.' : 'Archive-only build mode. Enable publish_images locally to produce registrable signed images.'));
      root.append(connection);
      const trust = card(`Build signing trust · ${settings.key_count} public keys`);
      for (const key of settings.keys || []) trust.append(el('code', key.id), el('p', key.public_key));
      if (settings.can_add_key) {
        const form = el('form'), label = el('label', 'Separate CI Ed25519 public key (base64)'), input = el('input');
        input.name = 'public_key'; input.required = true; input.autocomplete = 'off'; label.append(input);
        const submit = el('button', 'Add public key'); submit.dataset.mutation = 'key';
        form.append(label, submit); form.onsubmit = async e => { e.preventDefault(); await action('key', 'release-settings/keys', {public_key: input.value.trim()}); }; trust.append(form);
      }
      root.append(trust); return;
    }
    const tabs = el('div', undefined, 'release-tabs'); tabs.setAttribute('role', 'tablist');
    for (const [value, label] of [['builds', '1 · Builds'], ['versions', '2 · Trusted versions & images'], ['upgrade', '3 · Upgrade tasks']]) {
      const b = button(label, () => navigate({tab: value})); b.setAttribute('role', 'tab'); b.setAttribute('aria-selected', String(selected.tab === value)); tabs.append(b);
    }
    root.replaceChildren(tabs, button('Release settings', () => go('release-settings')));
    if (selected.tab === 'builds') {
      const data = await api('builds'); if (!isCurrent()) return;
      if (!data.enabled) { root.append(el('p', 'Configure an approved GitHub workflow in Release settings.')); return; }
      root.append(el('p', `${data.config.repository} / ${data.config.workflow} · ${data.build_trust.key_count} build public keys`));
      if (data.build_trust.error_code) root.append(button('Configure build public keys', () => go('release-settings')));
      for (const run of data.runs || []) {
        const key = `register-${run.id}-${run.run_attempt}`, reg = run.registration || {status: 'unknown'};
        if (checking.has(key) && ['registered','unregistered'].includes(reg.status)) { uncertain.delete(key); checking.delete(key); }
        const box = card(`Build #${run.run_number} · attempt ${run.run_attempt || '?'}`);
        box.append(el('p', DistributionUI.buildTime(run.run_started_at)),el('p',run.commit_message||'Commit message unavailable'),el('p', `${run.head_branch} · ${run.head_sha}`), el('p', `Build: ${run.status} / ${run.conclusion || 'pending'}`), el('strong', `Registration: ${reg.status}`, reg.status === 'registered' ? 'status-ready' : ''));
        if (reg.status === 'registered') {
          box.append(el('p', `${reg.release_id} · ${reg.registered_at}`));
          for (const image of reg.images || []) box.append(el('p', `${image.service} · ${image.platform}`));
          box.append(button('View version', () => navigate({tab: 'versions', version: reg.release_id, run: String(run.id)})));
        } else {
          box.append(el('p', run.reason || 'Image scope remains unverified until registration.'));
          const b = button(uncertain.has(key) ? 'Check registration first' : 'Register signed artifact', () => {
            if (uncertain.has(key)) { checking.add(key); reload(); return; }
            history.replaceState(null, '', locationFor({...selected, run: String(run.id)}));
            action(key, `builds/${run.id}/register`, {attempt: run.run_attempt});
          }, pending.has(key) || (!uncertain.has(key) && !canRegister(run, allowed('build.write'))));
          b.dataset.mutation = key; box.append(b);
        }
        root.append(box);
      }
      if (!(data.runs || []).length) root.append(el('p', 'No runs on approved refs.'));
      return;
    }
    const versions = await api('releases'); if (!isCurrent()) return;
    if (selected.version && !versions.some(v=>v.id===selected.version)) { versions.push(await api('releases/'+encodeURIComponent(selected.version))); if (!isCurrent()) return; }
    const picker = el('select'); picker.setAttribute('aria-label', 'Trusted version');
    picker.append(new Option('Choose a verified version', ''));
    for (const v of versions) picker.append(new Option(v.id, v.id));
    picker.value = versions.some(v => v.id === selected.version) ? selected.version : '';
    picker.onchange = () => navigate({version: picker.value}); root.append(picker);
    const version = versions.find(v => v.id === selected.version); if (!version) return;
    const provenance = card(version.id); provenance.append(el('p', `${version.manifest.build.repository} · ${version.manifest.build.source_commit} · attempt ${version.manifest.build.run_attempt}`));
    for (const i of version.manifest.images) provenance.append(el('p', `${i.service} · ${i.platform}`), el('code', i.reference));
    root.append(provenance);
    const groups = await api(`releases/${encodeURIComponent(version.id)}/admin-preparation`); if (!isCurrent()) return;
    const target = el('select'); target.setAttribute('aria-label', 'Managed admin host'); target.append(new Option(selected.host && !groups.some(g => g.host_id === selected.host) ? 'Previous host unavailable; choose explicitly' : 'Choose target host', ''));
    for (const g of groups) target.append(new Option(`${g.host_id} · ${g.status}`, g.host_id));
    target.value = selectHost(groups, selected.host);
    if (!selected.host && target.value) { selected.host = target.value; history.replaceState(null, '', locationFor(selected)); }
    target.onchange = () => navigate({host: target.value}); root.append(target);
    const group = groups.find(g => g.host_id === target.value); if (!group) return;
    const box = card(`Admin image preparation · ${group.status}`); box.append(el('p', group.reason));
    for (const t of group.targets) {
      box.append(el('h4', `${t.kind} / ${t.service}`), el('p', `${t.platform} · ${t.status}`), el('code', t.image));
      if (t.image_id) box.append(el('p', `Verified Docker ID: ${t.image_id}`));
      if (t.verified_at) box.append(el('p', `Verified: ${t.verified_at}`));
      if (t.error_code) box.append(el('p', t.error_code));
    }
    const key = `prepare-${group.batch}`;
    if (checking.has(key)) { uncertain.delete(key); checking.delete(key); }
    if (selected.tab === 'versions') {
      const b = button(uncertain.has(key) ? 'Check preparation status' : group.status === 'failed' ? 'Retry failed items' : group.status === 'unknown' ? 'Recheck cached images' : 'Prepare API + Web images', () => {
        if (uncertain.has(key)) { checking.add(key); reload(); return; }
        action(key, `releases/${encodeURIComponent(version.id)}/prepare-admin`, {host_id: group.host_id, batch: group.batch});
      }, pending.has(key) || (!uncertain.has(key) && !group.can_prepare)); b.dataset.mutation = key; box.append(b);
      box.append(button('Next: upgrade tasks', () => navigate({tab: 'upgrade'}), group.status !== 'ready'));
    } else {
      box.append(el('p', 'Prepared images do not authorize an upgrade. API/Web publications use one recovery unit and an independent approver. No configuration or database migration is automatic.'));
      box.append(button('Prepare / recheck images', () => navigate({tab: 'versions'})));
      for (const t of group.targets) box.append(button(`Review ${t.kind} publication`, () => ctx.createUpgrade(t.service, t.image), group.status !== 'ready' || !allowed('ops.write')));
    }
    root.append(box);
  }
  return {render, state, locationFor, selectHost, canRegister, get busy() { return pending.size > 0; }};
})();
if (typeof module !== 'undefined') module.exports = ReleaseUI;
