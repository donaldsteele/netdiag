document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    ifaces: [], ifaceName: '', cidr: '', community: 'public',
    scanId: '', running: false, es: null,
    hosts: [], gateway: '', warnings: [], state: '',
    stage: '', done: 0, total: 0, error: '', quitted: false,
    filter: 'all', sortKey: 'ip', sortDir: 1, open: {},
    live: true, liveStatus: null, nowMs: Date.now(), announce: '', announced: {}, liveSynced: false,
    view: 'hosts', health: null, healthOpen: true, healthBusy: false, healthMsg: '',
    conflicts: [], banners: [], seenNotices: {},
    settings: null, draft: null, settingsError: '', storeDown: false, desktopNote: '', audio: null,
    history: [], historyError: '', baseId: '', currentId: '', diff: null, diffError: '', diffBusy: false,
    tabs: [{id: 'all', label: 'All'}, {id: 'printers', label: 'Printers'},
           {id: 'web', label: 'Web'}, {id: 'problems', label: 'Problems'}, {id: 'new', label: 'New'},
           {id: 'offline', label: 'Offline'}, {id: 'conflicts', label: 'Conflicts'}],

    async init() {
      try { const v = localStorage.getItem('netdiag.live'); if (v !== null) this.live = v === '1'; } catch (e) {}
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
      this.draft = JSON.parse(JSON.stringify(this.settings || {community: '', webServer: {}, alerts: {triggers: {}}, ignoredConflictMACs: []}));
      this.draft.ignoredConflictMACs = this.draft.ignoredConflictMACs || [];
      this.$nextTick(() => { const d = this.$refs.dlg; if (d && !d.open) d.showModal(); });
    },
    closeSettings() { const d = this.$refs.dlg; if (d && d.open) d.close(); },
    async saveSettings() {
      this.settingsError = '';
      const n = v => (v === '' || v === null ? 0 : Number(v));
      this.draft.webServer.expectStatus = n(this.draft.webServer.expectStatus);
      this.draft.alerts.supplyThresholdPct = n(this.draft.alerts.supplyThresholdPct);
      this.draft.alerts.cooldownSec = n(this.draft.alerts.cooldownSec);
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
      this.view = v;
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
    pickIface() {
      const i = this.ifaces.find(x => x.name === this.ifaceName);
      if (i) this.cidr = i.suggested || i.cidr;
    },
    async post(url, body) {
      return fetch(url, {method: 'POST', headers: {'Content-Type': 'application/json'},
                         body: body === undefined ? undefined : JSON.stringify(body)});
    },
    async start() {
      this.error = ''; this.hosts = []; this.warnings = []; this.open = {};
      this.stage = ''; this.done = 0; this.total = 0; this.state = ''; this.gateway = '';
      this.liveStatus = null; this.announce = ''; this.announced = {}; this.conflicts = [];
      this.seenNotices = {};
      try {
        const r = await this.post('/api/scans', {cidr: this.cidr.trim(), interface: this.ifaceName, community: this.community, live: !!this.live});
        const j = await r.json();
        if (!r.ok) { this.error = j.error || ('Scan failed (HTTP ' + r.status + ')'); return; }
        this.scanId = j.id; this.running = true; this.state = 'running';
        this.listen(j.id);
      } catch (e) { this.error = 'Could not start scan: ' + e.message; }
    },
    listen(id) {
      if (this.es) this.es.close();
      const es = new EventSource('/api/scans/' + id + '/events');
      this.es = es; this.liveSynced = false;
      const on = (t, fn) => es.addEventListener(t, ev => { try { fn(JSON.parse(ev.data)); } catch (e) {} });
      on('host', h => this.upsert(h));
      on('health', r => { this.health = r; });
      on('conflict', ev => this.onConflict(ev));
      on('notice', n => this.onNotice(n, id));
      on('progress', p => { this.stage = p.stage; this.done = p.done; this.total = p.total; });
      on('stage', s => { this.stage = s.stage; this.done = 0; this.total = 0; });
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
        this.liveSynced = true;
      });
      // Presence events are live-only (never replayed), so a reconnect does
      // not re-announce old transitions.
      on('presence', p => {
        const h = this.hosts.find(x => x.ip === p.ip) || {};
        this.announce = p.online ? 'Device back online: ' + p.ip
                                 : ('Device went offline: ' + p.ip + ' ' + (h.vendor || '')).trim();
      });
      on('done', r => {
        this.liveStatus = null;
        if (r.health) this.health = r.health;
        if (r.conflicts) this.conflicts = r.conflicts;
        this.hosts = r.hosts || this.hosts; this.gateway = r.gateway || ''; this.warnings = r.warnings || [];
        this.state = r.state; this.running = false; this.stage = 'finished';
        es.close(); this.es = null;
      });
      on('error', m => { this.error = (m && m.message) || 'Scan error'; });
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
      return 'Choose a target and press Scan.';
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
      if (f === 'printers') return h.isPrinter;
      if (f === 'web') return (h.web || []).length > 0;
      if (f === 'problems') return this.isProblem(h);
      if (f === 'new') return !!h.isNew;
      if (f === 'conflicts') return this.hasConflict(h);
      if (f === 'offline') return this.isOffline(h);
      return true;
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
      return this.hosts.filter(h => this.matches(h, this.filter)).sort((a, b) => {
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
        last = 'last check ' + s + 's ago';
      }
      return 'Live · watching ' + (st.unseen || 0) + ' addresses · ' + last + ' · ' + (st.newHosts || 0) + ' new' +
        ' · ' + (st.offline || 0) + ' offline';
    },
    isOpen(ip) { return !!this.open[ip]; },
    toggle(ip) { this.open[ip] = !this.open[ip]; },

    printerSource(p) {
      return {ipp: 'via IPP', snmp: 'via SNMP', 'snmp+ipp': 'via SNMP + IPP'}[p && p.source] || '';
    },
    mdnsName(h) { return h.mdns ? (h.mdns.instance || h.mdns.hostname || '') : ''; },
    statusText(h) {
      const parts = [this.isOffline(h) ? 'offline' : h.alive ? 'up' : 'no ping'];
      if (h.isPrinter && h.printer) {
        if (h.printer.model) parts.push(h.printer.model);
        const bad = (h.printer.alerts || []).length;
        if (bad) parts.push(bad + ' alert' + (bad > 1 ? 's' : ''));
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
