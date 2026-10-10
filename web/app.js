document.addEventListener('alpine:init', () => {
  Alpine.data('app', () => ({
    ifaces: [], ifaceName: '', cidr: '', community: 'public',
    scanId: '', running: false, es: null,
    hosts: [], gateway: '', warnings: [], state: '',
    stage: '', done: 0, total: 0, error: '', quitted: false,
    filter: 'all', sortKey: 'ip', sortDir: 1, open: {},
    live: true, liveStatus: null, nowMs: Date.now(), announce: '', announced: {}, liveSynced: false,
    tabs: [{id: 'all', label: 'All'}, {id: 'printers', label: 'Printers'},
           {id: 'web', label: 'Web'}, {id: 'problems', label: 'Problems'}, {id: 'new', label: 'New'},
           {id: 'offline', label: 'Offline'}],

    async init() {
      try { const v = localStorage.getItem('netdiag.live'); if (v !== null) this.live = v === '1'; } catch (e) {}
      setInterval(() => { this.nowMs = Date.now(); }, 1000);
      try {
        const r = await fetch('/api/interfaces');
        this.ifaces = (await r.json()) || [];
        const d = this.ifaces.find(i => i.isDefault) || this.ifaces[0];
        if (d) { this.ifaceName = d.name; this.cidr = d.suggested || d.cidr; }
      } catch (e) { this.error = 'Could not list network interfaces: ' + e.message; }
    },
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
      this.liveStatus = null; this.announce = ''; this.announced = {};
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
      on('progress', p => { this.stage = p.stage; this.done = p.done; this.total = p.total; });
      on('stage', s => { this.stage = s.stage; this.done = 0; this.total = 0; });
      on('snapshot', r => {
        this.gateway = r.gateway || ''; this.warnings = r.warnings || []; this.state = r.state;
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
