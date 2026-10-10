let tileRaf = 0;
let tileCur = {}; // non-reactive copy of the tile values, so tweening does not retrigger its own effect
document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    ifaces: [], ifaceName: '', cidr: '', community: 'public',
    scanId: '', running: false, es: null,
    hosts: [], gateway: '', warnings: [], state: '',
    stage: '', done: 0, total: 0, error: '', quitted: false,
    filter: 'all', sortKey: 'ip', sortDir: 1, drawerIP: '', search: '',
    live: true, liveStatus: null, nowMs: Date.now(), announce: '', announced: {}, liveSynced: false,
    theme: document.documentElement.getAttribute('data-theme') === 'light' ? 'light' : 'dark',
    view: 'devices', navOpen: false, views: ['devices', 'health', 'activity', 'history'], health: null, healthOpen: true, healthBusy: false, healthMsg: '',
    conflicts: [], banners: [], seenNotices: {},
    settings: null, draft: null, settingsError: '', storeDown: false, desktopNote: '', audio: null,
    history: [], historyError: '', baseId: '', currentId: '', diff: null, diffError: '', diffBusy: false,
    shown: {},
    log: [], logSeq: 0, logHidden: {}, logStick: true, logSeen: {},
    logKinds: [{id: 'scan', label: 'Scan'}, {id: 'host', label: 'New host'}, {id: 'offline', label: 'Offline'}, {id: 'online', label: 'Online'},
               {id: 'conflict', label: 'Conflict'}, {id: 'health', label: 'Health'}, {id: 'notice', label: 'Notice'}, {id: 'error', label: 'Error'}],
    logPush(kind, text, ip) {
      this.log.push({id: ++this.logSeq, t: new Date().toISOString(), kind: kind, ip: ip || '', text: text});
      if (this.log.length > 500) this.log.splice(0, this.log.length - 500);
    },
    logShown() { return this.log.filter(e => !this.logHidden[e.kind]); },
    logToggle(k) { this.logHidden[k] = !this.logHidden[k]; },
    logTime(e) { return new Date(e.t).toTimeString().slice(0, 8); },
    logLine(e) { return this.logTime(e) + '  ' + e.kind.toUpperCase().padEnd(8) + ' ' + e.text; },
    logText() { return this.logShown().map(e => this.logLine(e)).join('\n'); },
    logRecent() { return this.log.slice(-3); },
    logOnScroll(el) { this.logStick = el.scrollTop + el.clientHeight >= el.scrollHeight - 8; },
    logFollow(el) { if (el && this.logStick) this.$nextTick(() => { el.scrollTop = el.scrollHeight; }); },
    logHealth(prev, cur) {
      if (!prev || !cur) return;
      const old = {};
      (prev.checks || []).forEach(c => { old[c.id] = c.status; });
      (cur.checks || []).forEach(c => {
        if (old[c.id] !== undefined && old[c.id] !== c.status) this.logPush('health', (c.label || c.id) + ': ' + old[c.id] + ' -> ' + c.status);
      });
    },
    tileDefs: [
      {id: 'alive', label: 'Alive', filter: 'alive', icon: 'wifi'}, {id: 'printers', label: 'Printers', filter: 'printers', icon: 'printer'},
      {id: 'web', label: 'Web', filter: 'web', icon: 'globe'}, {id: 'mdns', label: 'mDNS', filter: 'mdns', icon: 'radio-tower'},
      {id: 'offline', label: 'Offline', filter: 'offline', icon: 'wifi-off'}, {id: 'conflicts', label: 'Conflicts', filter: 'conflicts', icon: 'triangle-alert'}],
    tileCounts() {
      const c = {alive: 0, printers: 0, web: 0, mdns: 0, offline: 0, conflicts: 0};
      for (const h of this.hosts) {
        if (h.alive) c.alive++;
        if (h.isPrinter) c.printers++;
        if ((h.web || []).length) c.web++;
        if (h.mdns) c.mdns++;
        if (this.isOffline(h)) c.offline++;
        if (this.hasConflict(h)) c.conflicts++;
      }
      return c;
    },
    tileTone(t) {
      const n = this.shown[t.id] || 0;
      if (t.id === 'conflicts' && n > 0) return 'tone-bad';
      if (t.id === 'offline' && n > 0) return 'tone-warn';
      return '';
    },
    tileClick(t) { this.filter = this.filter === t.filter ? 'all' : t.filter; },
    tweenTiles(target) {
      let reduce = false;
      try { reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches; } catch (e) {}
      cancelAnimationFrame(tileRaf);
      const from = {...tileCur}, t0 = performance.now(), dur = 350;
      const same = Object.keys(target).every(k => from[k] === target[k]);
      if (reduce || same) { tileCur = {...target}; this.shown = {...target}; return; }
      const step = (now) => {
        const p = Math.min(1, (now - t0) / dur), o = {};
        for (const k in target) o[k] = Math.round((from[k] || 0) + (target[k] - (from[k] || 0)) * p);
        tileCur = o; this.shown = o;
        if (p < 1) tileRaf = requestAnimationFrame(step);
      };
      tileRaf = requestAnimationFrame(step);
    },
    healthStrip() {
      const by = {};
      for (const c of ((this.health || {}).checks || [])) by[c.id] = c;
      const ids = [['gateway', 'Gateway'], ['dhcp', 'DHCP'], ['dns', 'DNS'], ['internet', 'Internet'], ['wifi', 'Wi-Fi'], ['webserver', 'Event web']];
      return ids.map(([id, label]) => {
        const c = by[id];
        if (!c) return {id, label, icon: this.checkIcon(id), status: 'unavailable', value: 'not checked'};
        const lat = this.healthLatency(c);
        return {id, label, icon: this.checkIcon(id), status: c.status, value: this.healthStatusText(c.status) + (lat ? ' ' + lat : '')};
      });
    },
    tabs: [{id: 'all', label: 'All'}, {id: 'printers', label: 'Printers'},
           {id: 'web', label: 'Web'}, {id: 'problems', label: 'Problems'}, {id: 'new', label: 'New'},
           {id: 'offline', label: 'Offline'}, {id: 'conflicts', label: 'Conflicts'}],

    async init() {
      try { const v = localStorage.getItem('netdiag.live'); if (v !== null) this.live = v === '1'; } catch (e) {}
      try { const sv = localStorage.getItem('netdiag.view'); if (this.views.includes(sv)) this.view = sv; } catch (e) {}
      if (this.view === 'history') this.loadHistory();
      setInterval(() => { this.nowMs = Date.now(); }, 1000);
      try {
        const r = await fetch('/api/interfaces');
        this.ifaces = (await r.json()) || [];
        const d = this.ifaces.find(i => i.isDefault) || this.ifaces[0];
        if (d) { this.ifaceName = d.name; this.cidr = d.suggested || d.cidr; }
      } catch (e) { this.error = 'Could not list network interfaces: ' + e.message; }
      this.loadSettings();
      this.loadHealth();
    },
    async loadSettings() {
      try {
        const r = await fetch('/api/settings');
        if (r.status === 503) { this.storeDown = true; return; }
        if (!r.ok) return;
        this.settings = await r.json();
        if (this.settings.community) this.community = this.settings.community;
      } catch (e) {}
    },
    async loadHealth() {
      try {
        const r = await fetch('/api/health');
        if (r.ok) this.health = await r.json();
      } catch (e) {}
    },
    async recheck() {
      this.healthBusy = true; this.healthMsg = 'Running checks...';
      try {
        const r = await this.post('/api/health/run', {interface: this.ifaceName});
        if (r.ok) { this.health = await r.json(); this.healthMsg = 'Checks updated.'; }
        else { this.healthMsg = 'Health check failed (HTTP ' + r.status + ').'; }
      } catch (e) { this.healthMsg = 'Health check failed: ' + e.message; }
      this.healthBusy = false;
    },
    healthIcon(s) { return {ok: '✔', warn: '▲', fail: '✖', info: 'ℹ'}[s] || '–'; },
    healthStatusText(s) { return {ok: 'OK', warn: 'Warning', fail: 'Failed', info: 'Info', unavailable: 'Unavailable'}[s] || s; },
    healthSummary() {
      if (!this.health) return 'Health: not checked';
      const c = this.health.checks || [];
      const f = c.filter(x => x.status === 'fail').length, w = c.filter(x => x.status === 'warn').length;
      if (!f && !w) return 'Health: OK';
      return 'Health: ' + [f ? f + ' fail' : '', w ? w + ' warn' : ''].filter(Boolean).join(', ');
    },
    healthLatency(c) { return c.latencyMs > 0 ? c.latencyMs.toFixed(1) + ' ms' : ''; },

    // ---- settings ----
    openSettings() {
      if (!this.settings) { this.settingsError = this.storeDown ? 'The data store is unavailable, so settings cannot be changed.' : 'Settings are not loaded yet.'; }
      else { this.settingsError = ''; }
      this.draft = JSON.parse(JSON.stringify(this.settings || {community: '', webServer: {}, alerts: {triggers: {}}, ignoredConflictMACs: [], mcp: {enabled: false}}));
      this.draft.ignoredConflictMACs = this.draft.ignoredConflictMACs || [];
      this.draft.mcp = this.draft.mcp || {enabled: false};
      this.draft.mcp.enabled = !!this.draft.mcp.enabled;
      this.$nextTick(() => { const d = this.$refs.dlg; if (d && !d.open) d.showModal(); });
    },
    closeSettings() { const d = this.$refs.dlg; if (d && d.open) d.close(); },
    async saveSettings() {
      this.settingsError = '';
      const n = v => (v === '' || v === null ? 0 : Number(v));
      this.draft.webServer.expectStatus = n(this.draft.webServer.expectStatus);
      this.draft.alerts.supplyThresholdPct = n(this.draft.alerts.supplyThresholdPct);
      this.draft.alerts.cooldownSec = n(this.draft.alerts.cooldownSec);
      this.draft.printerStatusPollSec = n(this.draft.printerStatusPollSec);
      const ok = await this.putSettings(this.draft, true);
      if (ok) this.closeSettings();
    },
    async putSettings(doc, showErr) {
      try {
        const r = await fetch('/api/settings', {method: 'PUT', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(doc)});
        if (r.status === 503) { this.storeDown = true; if (showErr) this.settingsError = 'The data store is unavailable.'; return false; }
        const j = await r.json().catch(() => ({}));
        if (!r.ok) { if (showErr) this.settingsError = j.error || ('Could not save settings (HTTP ' + r.status + ')'); return false; }
        this.settings = j;
        if (j.community) this.community = j.community;
        return true;
      } catch (e) { if (showErr) this.settingsError = 'Could not save settings: ' + e.message; return false; }
    },
    removeIgnored(i) { this.draft.ignoredConflictMACs.splice(i, 1); },
    soundOn() { return !this.settings || this.settings.alerts.sound; },
    async toggleSound() {
      if (!this.settings) return;
      const doc = JSON.parse(JSON.stringify(this.settings));
      doc.alerts.sound = !doc.alerts.sound;
      if (doc.alerts.sound) this.beep('info');
      await this.putSettings(doc, false);
    },
    desktopState() {
      if (typeof Notification === 'undefined') return 'unsupported';
      return Notification.permission;
    },
    async enableDesktop() {
      this.desktopNote = '';
      if (typeof Notification === 'undefined') { this.desktopNote = 'This browser does not support desktop notifications.'; return; }
      let p = Notification.permission;
      if (p === 'default') p = await Notification.requestPermission();
      if (p !== 'granted') { this.desktopNote = 'Desktop alerts are blocked. Allow notifications for this site in your browser settings.'; return; }
      this.desktopNote = 'Desktop alerts enabled.';
      if (this.settings && !this.settings.alerts.desktop) {
        const doc = JSON.parse(JSON.stringify(this.settings)); doc.alerts.desktop = true;
        await this.putSettings(doc, false);
      }
    },

    // ---- alerts ----
    onNotice(n, scanId) {
      // Notice ids (n-<seq>) restart per scan, so the dedupe key includes the scan id.
      if (!n || n.replay || !n.id) return; // replayed notices stay silent
      const key = (scanId || '') + ':' + n.id;
      if (this.seenNotices[key]) return;
      this.seenNotices[key] = true;
      this.logPush('notice', (n.title || 'Notice') + (n.message ? ': ' + n.message : ''), n.ip);
      this.banners.unshift(Object.assign({}, n, {bid: key}));
      if (this.banners.length > 50) this.banners.length = 50;
      if (this.soundOn()) this.beep(n.severity);
      if (this.settings && this.settings.alerts.desktop && this.desktopState() === 'granted') {
        try { new Notification(n.title || 'netdiag', {body: n.message || '', tag: n.key || n.id}); } catch (e) {}
      }
    },
    shownBanners() { return this.banners.slice(0, 3); },
    moreBanners() { return Math.max(0, this.banners.length - 3); },
    dismiss(bid) { this.banners = this.banners.filter(b => b.bid !== bid); },
    dismissAll() { this.banners = []; },
    beep(sev) {
      try {
        const AC = window.AudioContext || window.webkitAudioContext;
        if (!AC) return;
        if (!this.audio) this.audio = new AC();
        const ctx = this.audio, seq = sev === 'critical' ? [880, 660, 880] : sev === 'warn' ? [523] : [440];
        let t = ctx.currentTime;
        seq.forEach(f => {
          const o = ctx.createOscillator(), g = ctx.createGain();
          o.type = 'sine'; o.frequency.value = f; g.gain.value = 0.08;
          o.connect(g); g.connect(ctx.destination);
          o.start(t); o.stop(t + 0.16); t += 0.2;
        });
      } catch (e) {}
    },

    // ---- conflicts ----
    conflictsFor(h) {
      const ids = h.conflicts || [];
      return this.conflicts.filter(c => ids.includes(c.id) || (c.ips || []).includes(h.ip));
    },
    hasConflict(h) { return this.conflictsFor(h).length > 0; },
    conflictKind(c) {
      return {mac_flap: 'The MAC address for this IP keeps changing - two devices may share the IP, or a device is switching adapters.',
              mac_multi_ip: 'One MAC address answers for several IPs - often a range extender or router, or a device with several addresses.',
              duplicate_name: 'More than one device uses the same name.'}[c.kind] || c.kind;
    },
    onConflict(ev) {
      if (!ev || !ev.conflict) return;
      const id = ev.conflict.id;
      this.conflicts = this.conflicts.filter(c => c.id !== id);
      if (!ev.resolved) this.conflicts.push(ev.conflict);
      const k = id + (ev.resolved ? ':r' : '');
      if (!this.logSeen[k]) {
        this.logSeen[k] = true;
        const c = ev.conflict;
        this.logPush('conflict', (ev.resolved ? 'Conflict resolved: ' : 'Conflict: ') + (c.detail || c.kind), (c.ips || [])[0]);
      }
    },
    async ignoreMac(mac) {
      try {
        const r = await this.post('/api/conflicts/ignore', {mac: mac});
        if (r.status === 503) { this.error = 'The data store is unavailable, so the MAC cannot be ignored.'; return; }
        if (!r.ok) { this.error = 'Could not ignore MAC (HTTP ' + r.status + ')'; return; }
        this.announce = 'Ignoring ' + mac;
        this.loadSettings();
        this.conflicts = this.conflicts.filter(c => !(c.macs || []).includes(mac));
      } catch (e) { this.error = 'Could not ignore MAC: ' + e.message; }
    },

    // ---- history & compare ----
    async showView(v) {
      if (!this.views.includes(v)) v = 'devices';
      this.view = v;
      this.navOpen = false;
      try { localStorage.setItem('netdiag.view', v); } catch (e) {}
      if (v === 'history') await this.loadHistory();
    },
    async loadHistory() {
      this.historyError = '';
      try {
        const r = await fetch('/api/history');
        if (r.status === 503) { this.storeDown = true; this.historyError = 'The data store is unavailable, so history cannot be shown.'; this.history = []; return; }
        if (!r.ok) { this.historyError = 'Could not load history (HTTP ' + r.status + ')'; return; }
        this.history = (await r.json()) || [];
        const ok = this.readableHistory();
        if (!this.currentId && ok.length) this.currentId = ok[0].id;
      } catch (e) { this.historyError = 'Could not load history: ' + e.message; }
    },
    async deleteHistory(id) {
      if (!window.confirm('Delete this saved scan?')) return;
      const r = await fetch('/api/history/' + encodeURIComponent(id), {method: 'DELETE'});
      if (!r.ok && r.status !== 404) { this.historyError = 'Could not delete (HTTP ' + r.status + ')'; return; }
      if (this.currentId === id) this.currentId = '';
      if (this.baseId === id) this.baseId = '';
      this.diff = null;
      await this.loadHistory();
    },
    readableHistory() { return this.history.filter(h => !h.error); },
    currentOptions() {
      const o = this.readableHistory().map(h => ({id: h.id, label: this.histLabel(h)}));
      if (this.scanId && !o.some(x => x.id === this.scanId)) o.unshift({id: this.scanId, label: 'Current scan (' + this.scanId + ')'});
      return o;
    },
    histLabel(h) { return h.startedAt + ' - ' + h.cidr + (h.live ? ' (live)' : ''); },
    compareQuery() {
      const q = new URLSearchParams();
      if (this.baseId) q.set('base', this.baseId);
      q.set('current', this.currentId);
      return q;
    },
    async runCompare() {
      this.diff = null; this.diffError = '';
      if (!this.currentId) { this.diffError = 'Choose a scan to compare.'; return; }
      this.diffBusy = true;
      try {
        const r = await fetch('/api/compare?' + this.compareQuery().toString());
        const j = await r.json().catch(() => ({}));
        if (r.status === 503) this.diffError = 'The data store is unavailable.';
        else if (!r.ok) this.diffError = j.error || ('Compare failed (HTTP ' + r.status + ')');
        else this.diff = j;
      } catch (e) { this.diffError = 'Compare failed: ' + e.message; }
      this.diffBusy = false;
    },
    exportDiff(f) {
      if (!this.currentId) return;
      const q = this.compareQuery(); q.set('format', f);
      window.location.href = '/api/compare/export?' + q.toString();
    },
    changeText(c) { return c.field + ': ' + (c.from || '(none)') + ' → ' + (c.to || '(none)'); },
    statsText(s) { s = s || {}; return (s.alive || 0) + ' alive, ' + (s.printers || 0) + ' printers, ' + (s.web || 0) + ' web'; },
    saveLive() { try { localStorage.setItem('netdiag.live', this.live ? '1' : '0'); } catch (e) {} },
    toggleTheme() {
      this.theme = this.theme === 'light' ? 'dark' : 'light';
      document.documentElement.setAttribute('data-theme', this.theme);
      try { localStorage.setItem('netdiag.theme', this.theme); } catch (e) {}
    },
    ifaceTitle() { const i = this.ifaces.find(x => x.name === this.ifaceName); return i ? i.name + ' - ' + i.ip : 'Custom target'; },
    pickIface() {
      const i = this.ifaces.find(x => x.name === this.ifaceName);
      if (i) this.cidr = i.suggested || i.cidr;
    },
    async post(url, body) {
      return fetch(url, {method: 'POST', headers: {'Content-Type': 'application/json'},
                         body: body === undefined ? undefined : JSON.stringify(body)});
    },
    async start() {
      this.error = ''; this.hosts = []; this.warnings = []; this.drawerIP = '';
      this.stage = ''; this.done = 0; this.total = 0; this.state = ''; this.gateway = '';
      this.liveStatus = null; this.announce = ''; this.announced = {}; this.conflicts = [];
      this.seenNotices = {}; this.logSeen = {};
      try {
        const r = await this.post('/api/scans', {cidr: this.cidr.trim(), interface: this.ifaceName, community: this.community, live: !!this.live});
        const j = await r.json();
        if (!r.ok) { this.error = j.error || ('Scan failed (HTTP ' + r.status + ')'); return; }
        this.scanId = j.id; this.running = true; this.state = 'running';
        this.logPush('scan', 'Scan started: ' + this.cidr.trim() + (this.live ? ' (live)' : ''));
        this.listen(j.id);
      } catch (e) { this.error = 'Could not start scan: ' + e.message; }
    },
    listen(id) {
      if (this.es) this.es.close();
      const es = new EventSource('/api/scans/' + id + '/events');
      this.es = es; this.liveSynced = false;
      const on = (t, fn) => es.addEventListener(t, ev => { try { fn(JSON.parse(ev.data)); } catch (e) {} });
      on('host', h => {
        if (h.isNew && !this.logSeen['h:' + h.ip]) {
          this.logSeen['h:' + h.ip] = true;
          if (this.liveSynced) this.logPush('host', 'New device: ' + (h.ip + ' ' + (h.vendor || '')).trim(), h.ip);
        }
        this.upsert(h);
      });
      on('health', r => { this.logHealth(this.health, r); this.health = r; });
      on('conflict', ev => this.onConflict(ev));
      on('notice', n => this.onNotice(n, id));
      on('progress', p => { this.stage = p.stage; this.done = p.done; this.total = p.total; });
      on('stage', s => { this.stage = s.stage; this.done = 0; this.total = 0; this.logPush('scan', 'Stage: ' + s.stage); });
      on('snapshot', r => {
        this.gateway = r.gateway || ''; this.warnings = r.warnings || []; this.state = r.state;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        (r.hosts || []).forEach(h => { if (!this.hosts.some(x => x.ip === h.ip)) this.hosts.push(h); });
      });
      on('live', st => {
        this.liveStatus = st; this.state = 'live'; this.stage = 'live'; this.running = true;
        const fresh = this.hosts.filter(h => h.isNew && !this.announced[h.ip]);
        fresh.forEach(h => { this.announced[h.ip] = true; });
        // The first live event of a (re)connection only syncs; replayed hosts are not news.
        if (this.liveSynced && fresh.length) {
          this.announce = fresh.map(h => ('New device: ' + h.ip + ' ' + (h.vendor || '')).trim()).join('. ');
        }
        if (!this.liveSynced) this.logPush('scan', 'Live monitoring started');
        this.liveSynced = true;
      });
      // Presence events are live-only (never replayed), so a reconnect does
      // not re-announce old transitions.
      on('presence', p => {
        const h = this.hosts.find(x => x.ip === p.ip) || {};
        this.logPush(p.online ? 'online' : 'offline', p.online ? 'Back online: ' + p.ip : ('Went offline: ' + p.ip + ' ' + (h.vendor || '')).trim() + (p.reason ? ' (' + p.reason + ')' : ''), p.ip);
        this.announce = p.online ? 'Device back online: ' + p.ip
                                 : ('Device went offline: ' + p.ip + ' ' + (h.vendor || '')).trim();
      });
      on('done', r => {
        this.liveStatus = null;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        this.hosts = r.hosts || this.hosts; this.gateway = r.gateway || ''; this.warnings = r.warnings || [];
        this.state = r.state; this.running = false; this.stage = 'finished';
        this.logPush('scan', 'Scan ' + (r.state === 'cancelled' ? 'cancelled' : 'finished') + ': ' + this.hosts.length + ' hosts');
        es.close(); this.es = null;
      });
      on('error', m => { this.error = (m && m.message) || 'Scan error'; this.logPush('error', this.error); });
      es.onerror = () => { if (es.readyState === EventSource.CLOSED) this.running = false; };
    },
    upsert(h) {
      const i = this.hosts.findIndex(x => x.ip === h.ip);
      if (i >= 0) this.hosts[i] = h; else this.hosts.push(h);
    },
    async cancel() { if (this.scanId) await this.post('/api/scans/' + this.scanId + '/cancel'); },
    exportAs(f) { if (this.scanId) window.location.href = '/api/scans/' + this.scanId + '/export?format=' + f; },
    async quit() {
      if (this.es) this.es.close();
      try { await this.post('/api/quit'); } catch (e) {}
      this.running = false; this.quitted = true;
    },

    stageLabel() {
      const names = {icmp: 'Ping sweep', arp: 'ARP table', probe: 'Probing services',
                     live: 'Initial scan complete - watching for new devices',
                     finished: this.state === 'cancelled' ? 'Cancelled' : 'Finished'};
      if (!this.stage) return 'Idle';
      const n = names[this.stage] || this.stage;
      return this.total > 0 && this.stage !== 'finished' ? n + ' (' + this.done + '/' + this.total + ')' : n;
    },
    percent() {
      if (this.stage === 'live') return 100;
      if (this.stage === 'finished') return this.state === 'done' ? 100 : 0;
      return this.total > 0 ? Math.round(100 * this.done / this.total) : 0;
    },
    stats() {
      return {alive: this.hosts.filter(h => h.alive).length,
              printers: this.hosts.filter(h => h.isPrinter).length,
              web: this.hosts.filter(h => (h.web || []).length).length};
    },
    notices() {
      const n = [...this.warnings];
      if (this.state === 'done' || this.state === 'live') {
        const others = this.hosts.filter(h => h.alive && !h.isSelf && h.ip !== this.gateway);
        const gw = this.hosts.some(h => h.ip === this.gateway && h.alive);
        if (gw && !others.length) n.push('Only the router responded - client isolation may be enabled on this WiFi.');
      }
      return n;
    },
    emptyText() {
      if (this.state === 'live' && this.hosts.length) return 'No hosts match this filter.';
      if (this.state === 'live') return 'No hosts found yet. Watching for new devices...';
      if (this.running) return 'Scanning... hosts will appear as they are found.';
      if (this.state === 'done' && this.hosts.length) return 'No hosts match this filter.';
      if (this.state === 'done') return 'No hosts found.';
      return '> no scan yet - pick a target and press Scan';
    },

    isOffline(h) { return !!h.alive && h.online === false && !h.isSelf; },
    offlineFor(h) {
      const t = Date.parse(h.offlineSince || '');
      if (isNaN(t)) return 'offline';
      const s = Math.max(0, Math.round((this.nowMs - t) / 1000));
      if (s < 60) return 'offline for ' + s + 's';
      const m = Math.floor(s / 60);
      if (m < 60) return 'offline for ' + m + 'm';
      return 'offline for ' + Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
    },
    isProblem(h) {
      if (this.isOffline(h)) return true;
      if (this.hasConflict(h)) return true;
      const p = h.printer;
      if (h.isPrinter && p) {
        if ((p.alerts || []).length) return true;
        if ((p.supplies || []).some(s => s.state === 'empty' || s.state === 'low' || (s.percent >= 0 && s.percent <= 15))) return true;
      }
      return (h.web || []).some(w => w.error || !w.status || w.status >= 400);
    },
    matches(h, f) {
      if (f === 'alive') return !!h.alive;
      if (f === 'mdns') return !!h.mdns;
      if (f === 'printers') return h.isPrinter;
      if (f === 'web') return (h.web || []).length > 0;
      if (f === 'problems') return this.isProblem(h);
      if (f === 'new') return !!h.isNew;
      if (f === 'conflicts') return this.hasConflict(h);
      if (f === 'offline') return this.isOffline(h);
      return true;
    },
    hostText(h) {
      return [h.ip, h.hostname, this.mdnsName(h), h.mac, h.vendor].filter(Boolean).join(" ").toLowerCase();
    },
    count(f) { return this.hosts.filter(h => this.matches(h, f)).length; },
    ipNum(ip) { return ip.split('.').reduce((a, o) => a * 256 + (+o || 0), 0); },
    sortVal(h) {
      if (this.sortKey === 'vendor') return (h.vendor || '￿').toLowerCase();
      if (this.sortKey === 'status') return (h.isPrinter ? 0 : 1) * 10 + (h.alive ? 0 : 5) + (this.isProblem(h) ? -1 : 0);
      return this.ipNum(h.ip);
    },
    visible() {
      const k = this.sortDir;
      const q = this.search.trim().toLowerCase();
      return this.hosts.filter(h => this.matches(h, this.filter) && (!q || this.hostText(h).includes(q))).sort((a, b) => {
        const x = this.sortVal(a), y = this.sortVal(b);
        return (x < y ? -1 : x > y ? 1 : this.ipNum(a.ip) - this.ipNum(b.ip)) * k;
      });
    },
    sortBy(key) {
      if (this.sortKey === key) this.sortDir = -this.sortDir; else { this.sortKey = key; this.sortDir = 1; }
    },
    ariaSort(key) { return this.sortKey === key ? (this.sortDir > 0 ? 'ascending' : 'descending') : 'none'; },
    arrow(key) { return this.sortKey === key ? (this.sortDir > 0 ? ' ▲' : ' ▼') : ''; },
    liveText() {
      const st = this.liveStatus || {};
      let last = 'first check pending';
      if (st.lastCheckAt) {
        const s = Math.max(0, Math.round((this.nowMs - Date.parse(st.lastCheckAt)) / 1000));
        last = 'checked ' + s + 's ago';
      }
      return 'Live · ' + (st.newHosts || 0) + ' new · ' + (st.offline || 0) + ' offline' + ' · ' + last +
        ' · watching ' + (st.unseen || 0) + ' addresses';
    },
    typeInfo: {gateway: 'Gateway', printer: 'Printer', computer: 'Computer', phone: 'Phone/tablet', tv: 'TV/streamer', nas: 'NAS/storage',
               camera: 'Camera', network: 'Network gear', iot: 'Smart home/IoT', server: 'Server/web device', self: 'This computer', unknown: 'Unknown'},
    typeOf(h) {
      if (h.deviceType && this.typeInfo[h.deviceType]) return {icon: 'type-' + h.deviceType, label: this.typeInfo[h.deviceType]};
      if (h.isPrinter) return {icon: "type-printer", label: "Printer"};
      if (h.ip === this.gateway) return {icon: "type-gateway", label: "Gateway"};
      if (h.isSelf) return {icon: "type-self", label: "This computer"};
      if ((h.web || []).length) return {icon: "type-server", label: "Web server"};
      return {icon: "type-unknown", label: "Device"};
    },
    // Printer alerts outrank "up": critical/error alerts are bad, others warn.
    printerSev(h) {
      const al = (h.isPrinter && h.printer && h.printer.alerts) || [];
      if (al.some(a => a.severity === 'critical' || a.severity === 'error')) return 'bad';
      return al.length ? 'warn' : '';
    },
    statusIcon(h) {
      if (this.isOffline(h)) return 'circle-x';
      const p = this.printerSev(h);
      if (p) return p === 'bad' ? 'circle-x' : 'triangle-alert';
      return h.alive ? 'circle-check' : 'circle-help';
    },
    statusClass(h) {
      if (this.isOffline(h)) return 's-bad';
      const p = this.printerSev(h);
      if (p) return 's-' + p;
      return h.alive ? 's-ok' : 's-unknown';
    },
    statusShape(s) { return {ok: 'circle-check', warn: 'triangle-alert', fail: 'circle-x', info: 'info'}[s] || 'circle-help'; },
    statusShapeClass(s) { return {ok: 's-ok', warn: 's-warn', fail: 's-bad', info: 's-info'}[s] || 's-unknown'; },
    checkIcon(id) { return {gateway: 'router', dhcp: 'network', dns: 'book-search', internet: 'cloud', wifi: 'wifi', webserver: 'server'}[id] || 'activity'; },
    sevIcon(sev) { return {critical: 'circle-x', error: 'circle-x', warn: 'triangle-alert', info: 'info', ok: 'circle-check'}[sev] || 'info'; },
    sevClass(sev) { return {critical: 's-bad', error: 's-bad', warn: 's-warn', ok: 's-ok'}[sev] || 's-info'; },
    kindIcon(k) { return {scan: 'radar', host: 'circle-plus', offline: 'wifi-off', online: 'wifi', conflict: 'triangle-alert', health: 'heart-pulse', notice: 'bell', error: 'circle-x'}[k] || 'info'; },
    hostName(h) { return h.hostname || this.mdnsName(h); },
    dotClass(h) { return this.isOffline(h) ? "st-off" : h.alive ? "st-up" : "st-down"; },
    dotText(h) {
      if (this.isOffline(h)) return "Offline";
      const p = this.printerSev(h);
      const base = h.alive ? "Up" : "No ping";
      return p ? base + ", printer " + (p === "bad" ? "error" : "warning") : base;
    },
    drawerHost() { return this.hosts.find(h => h.ip === this.drawerIP) || null; },
    drawerHosts() { const h = this.drawerHost(); return h ? [h] : []; },
    rowClick(ev, ip) {
      if (ev.target.closest("a, button, input")) return;
      this.openDrawer(ip);
    },
    openDrawer(ip) {
      this.drawerIP = ip;
      this.$nextTick(() => { const b = this.$refs.drawerClose; if (b) b.focus(); });
    },
    closeDrawer(noFocus) {
      const ip = this.drawerIP;
      if (!ip) return;
      this.drawerIP = "";
      if (noFocus) return;
      this.$nextTick(() => {
        const rows = document.querySelectorAll("#hosts button.rowbtn");
        for (const b of rows) { if (b.dataset.ip === ip) { b.focus(); return; } }
        const m = document.getElementById("main"); if (m) m.focus();
      });
    },
    async copy(text, what) {
      try { await navigator.clipboard.writeText(text); }
      catch (e) {
        const t = document.createElement("textarea");
        t.value = text; t.setAttribute("readonly", ""); t.style.position = "fixed"; t.style.opacity = "0";
        document.body.appendChild(t); t.select();
        try { document.execCommand("copy"); } catch (e2) { /* ignore */ }
        document.body.removeChild(t);
      }
      this.announce = "Copied " + what + " " + text;
    },

    printerSource(p) {
      return {ipp: 'via IPP', snmp: 'via SNMP', 'snmp+ipp': 'via SNMP + IPP'}[p && p.source] || '';
    },
    mdnsName(h) { return h.mdns ? (h.mdns.instance || h.mdns.hostname || '') : ''; },
    statusText(h) {
      const parts = [this.isOffline(h) ? 'offline' : h.alive ? 'up' : 'no ping'];
      if (h.isPrinter && h.printer) {
        if (h.printer.model) parts.push(h.printer.model);
        const al = h.printer.alerts || [];
        if (al.length) parts.push(al.slice(0, 2).map(a => a.description || a.code).join(', ') + (al.length > 2 ? ' +' + (al.length - 2) + ' more' : ''));
        const low = (h.printer.supplies || []).filter(s => s.state === 'low' || s.state === 'empty').length;
        if (low) parts.push(low + ' supply low/empty');
      }
      return parts.join(' - ');
    },
    webNote(w) {
      if (w.error) return 'unreachable (' + w.error + ')';
      return [w.status ? 'HTTP ' + w.status : '', w.title, w.server].filter(Boolean).join(' - ');
    },
    supplyText(s) {
      const st = s.state && s.state !== 'ok' ? ' (' + s.state + ')' : '';
      return (s.percent >= 0 ? s.percent + '%' : 'unknown') + st;
    },
    trayLevel(t) {
      if (t.level < 0 || !t.max) return 'level unknown';
      return Math.round(100 * t.level / t.max) + '%';
    },
    snmpRows(h) {
      const s = h.snmp || {};
      const m = [['Version', s.version], ['Name', s.sysName], ['Description', s.sysDescr], ['Object ID', s.sysObjectID],
        ['Location', s.sysLocation], ['Contact', s.sysContact], ['Uptime', s.sysUpTime], ['Device', s.hrDeviceDescr],
        ['Device status', s.hrDeviceStatus], ['Printer status', s.hrPrinterStatus],
        ['Error state', (s.hrPrinterDetectedErrorState || []).join(', ')]];
      return m.filter(r => r[1]);
    },
  }));
});
