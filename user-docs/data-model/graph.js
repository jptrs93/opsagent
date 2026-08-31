/*
 * GraphKit — a tiny dependency-free framework for object-graph doc pages.
 *
 * Usage:
 *   const g = GraphKit.createGraph(mountEl);
 *   g.addRegion({x, y, w, h, title});
 *   g.addNote({x, y, html});
 *   g.addNode({id, title, badge, tone, desc, note, x, y, w, json | schema});
 *   g.addEdge({from, to, kind, label, fromSide, toSide, fromAt, toAt});
 *   g.render();
 *
 * The canvas pans (drag background), zooms (wheel / API), and cards can be
 * repositioned by dragging their header — edges follow live. Edge endpoints
 * anchor to a card side ('left'|'right'|'top'|'bottom', auto-chosen when
 * omitted) at a fractional position along that side (fromAt/toAt, default .5).
 *
 * A node with `schema` shows a type definition instead of literal JSON.
 * schema is a list of fields: {name, type, mod?, ref?, note?, key?, srv?,
 * versioned?} where srv marks a server-set field (rendered in the server-set
 * color), versioned tags the field's value as explicitly version-tracked, and
 * type is a scalar name string, {kind:'object', name, fields, versioned?}
 * (rendered inline, expanded by default, collapsible by clicking the type),
 * or {kind:'enum', name, values:[{name, note?}|string]} (rendered as an enum
 * chip, collapsed by default; hover for a value summary, click to expand).
 * A node or inline object with `versioned` is tagged as carrying its own
 * explicitly tracked version; the counter itself is implied, never a field.
 */
(function () {
  'use strict';

  var SVG = 'http://www.w3.org/2000/svg';

  function el(tag, cls, parent) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (parent) parent.appendChild(e);
    return e;
  }

  function svgEl(tag, parent) {
    var e = document.createElementNS(SVG, tag);
    if (parent) parent.appendChild(e);
    return e;
  }

  function clamp(v, lo, hi) { return v < lo ? lo : v > hi ? hi : v; }

  function esc(s) {
    return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');
  }

  // Lightweight JSON syntax highlighting over an already-formatted string.
  function highlightJSON(src) {
    return esc(src).replace(
      /("(?:[^"\\]|\\.)*")(\s*:)|("(?:[^"\\]|\\.)*")|\b(true|false|null)\b|(-?\d+(?:\.\d+)?)/g,
      function (m, key, colon, str, kw, num) {
        if (key) return '<span class="j-key">' + key + '</span>' + colon;
        if (str) return '<span class="j-str">' + str + '</span>';
        if (kw) return '<span class="j-kw">' + kw + '</span>';
        if (num) return '<span class="j-num">' + num + '</span>';
        return m;
      });
  }

  function renderFields(container, fields) {
    fields.forEach(function (f) {
      var row = el('div', 'gv-field', container);
      var line = el('div', 'gv-fline', row);
      el('span', f.srv ? 'f-name srv' : 'f-name', line).textContent = f.name;
      if (f.mod) el('span', 'f-mod', line).textContent = f.mod;
      var t = f.type;
      if (typeof t === 'string') {
        el('span', 'f-type', line).textContent = t;
      } else {
        var btn = el('button', 'f-toggle', line);
        btn.type = 'button';
        el('span', 'tw', btn).textContent = '▸';
        btn.appendChild(document.createTextNode(t.name));
        var kids = el('div', 'gv-fkids', row);
        if (t.kind === 'enum') {
          el('span', 'f-chip c-enum', line).textContent = 'enum';
          btn.title = t.values.map(function (v) { return v.name || v; }).join(' · ');
          t.values.forEach(function (v) {
            var vr = el('div', 'gv-eval', kids);
            el('span', 'e-name', vr).textContent = v.name || v;
            if (v.note) el('span', 'f-note', vr).textContent = v.note;
          });
        } else {
          row.classList.add('open');
          renderFields(kids, t.fields);
        }
      }
      if (f.versioned || (typeof t === 'object' && t.versioned)) {
        el('span', 'f-chip c-ver', line).textContent = 'versioned';
      }
      if (f.key) el('span', 'f-chip c-key', line).textContent = 'id';
      if (f.ref) el('span', 'f-ref', line).textContent = '→ ' + f.ref;
      if (f.note) el('span', 'f-note', line).textContent = f.note;
    });
  }

  function createGraph(mount) {
    var viewport = el('div', 'gv-viewport', mount);
    var world = el('div', 'gv-world', viewport);
    var svg = svgEl('svg', world);
    svg.setAttribute('class', 'gv-edges');

    var defs = svgEl('defs', svg);
    ['derive', 'ref'].forEach(function (kind) {
      var marker = svgEl('marker', defs);
      marker.setAttribute('id', 'gv-arrow-' + kind);
      marker.setAttribute('viewBox', '0 0 10 10');
      marker.setAttribute('refX', '9');
      marker.setAttribute('refY', '5');
      marker.setAttribute('markerWidth', '7');
      marker.setAttribute('markerHeight', '7');
      marker.setAttribute('orient', 'auto-start-reverse');
      var tip = svgEl('path', marker);
      tip.setAttribute('d', 'M 0 0 L 10 5 L 0 10 z');
      tip.setAttribute('class', 'm-' + kind);
    });
    var edgeLayer = svgEl('g', svg);

    // Labels live in a second SVG stacked above the cards so they never
    // disappear under one; render() moves it to the end of the world element.
    var svgTop = svgEl('svg');
    svgTop.setAttribute('class', 'gv-edges');
    var labelLayer = svgEl('g', svgTop);

    var nodes = [];
    var edges = [];
    var regions = [];
    var notes = [];
    var byId = {};
    var view = { x: 40, y: 20, s: 1 };
    var zoomListeners = [];

    function applyView() {
      world.style.transform =
        'translate(' + view.x + 'px, ' + view.y + 'px) scale(' + view.s + ')';
      zoomListeners.forEach(function (fn) { fn(view.s); });
    }

    /* ---------- content ---------- */

    function addRegion(r) { regions.push(r); return r; }
    function addNote(n) { notes.push(n); return n; }

    function addNode(n) {
      n.w = n.w || 360;
      nodes.push(n);
      byId[n.id] = n;
      return n;
    }

    function addEdge(e) {
      e.kind = e.kind || 'derive';
      edges.push(e);
      return e;
    }

    /* ---------- geometry ---------- */

    function nodeSize(n) {
      return { w: n.el.offsetWidth, h: n.el.offsetHeight };
    }

    function nodeCenter(n) {
      var s = nodeSize(n);
      return { x: n.x + s.w / 2, y: n.y + s.h / 2 };
    }

    function anchor(n, side, at) {
      var s = nodeSize(n);
      if (at == null) at = 0.5;
      switch (side) {
        case 'left':   return { x: n.x,       y: n.y + s.h * at, nx: -1, ny: 0 };
        case 'right':  return { x: n.x + s.w, y: n.y + s.h * at, nx: 1,  ny: 0 };
        case 'top':    return { x: n.x + s.w * at, y: n.y,       nx: 0,  ny: -1 };
        default:       return { x: n.x + s.w * at, y: n.y + s.h, nx: 0,  ny: 1 };
      }
    }

    function autoSides(a, b) {
      var ac = nodeCenter(a), bc = nodeCenter(b);
      var dx = bc.x - ac.x, dy = bc.y - ac.y;
      if (Math.abs(dx) >= Math.abs(dy)) {
        return dx >= 0 ? ['right', 'left'] : ['left', 'right'];
      }
      return dy >= 0 ? ['bottom', 'top'] : ['top', 'bottom'];
    }

    function drawEdges() {
      edgeLayer.textContent = '';
      labelLayer.textContent = '';
      edges.forEach(function (e) {
        var a = byId[e.from], b = byId[e.to];
        if (!a || !b || !a.el || !b.el) return;
        var sides = autoSides(a, b);
        var p0 = anchor(a, e.fromSide || sides[0], e.fromAt);
        var p3 = anchor(b, e.toSide || sides[1], e.toAt);
        var dist = Math.hypot(p3.x - p0.x, p3.y - p0.y);
        var k = clamp(dist * 0.35, 40, 180);
        var p1 = { x: p0.x + p0.nx * k, y: p0.y + p0.ny * k };
        var p2 = { x: p3.x + p3.nx * k, y: p3.y + p3.ny * k };

        var path = svgEl('path', edgeLayer);
        path.setAttribute('class', 'gv-edge k-' + e.kind);
        path.setAttribute('marker-end', 'url(#gv-arrow-' + e.kind + ')');
        path.setAttribute('d',
          'M ' + p0.x + ' ' + p0.y +
          ' C ' + p1.x + ' ' + p1.y + ', ' + p2.x + ' ' + p2.y +
          ', ' + p3.x + ' ' + p3.y);

        if (e.label) {
          // Place the label at parameter t along the curve (default midpoint);
          // labelAt lets an edge keep its label out from under other cards.
          var t = e.labelAt == null ? 0.5 : e.labelAt;
          var u = 1 - t;
          var mx = u * u * u * p0.x + 3 * u * u * t * p1.x + 3 * u * t * t * p2.x + t * t * t * p3.x;
          var my = u * u * u * p0.y + 3 * u * u * t * p1.y + 3 * u * t * t * p2.y + t * t * t * p3.y;
          var text = svgEl('text', labelLayer);
          text.setAttribute('class', 'gv-elabel');
          text.setAttribute('x', mx);
          text.setAttribute('y', my - 7);
          text.setAttribute('text-anchor', 'middle');
          var lines = Array.isArray(e.label) ? e.label : [e.label];
          lines.forEach(function (line, i) {
            var span = svgEl('tspan', text);
            span.setAttribute('x', mx);
            if (i > 0) span.setAttribute('dy', '13');
            span.textContent = line;
          });
        }
      });
    }

    function contentBounds() {
      var minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
      regions.forEach(function (r) {
        minX = Math.min(minX, r.x); minY = Math.min(minY, r.y);
        maxX = Math.max(maxX, r.x + r.w); maxY = Math.max(maxY, r.y + r.h);
      });
      nodes.forEach(function (n) {
        var s = nodeSize(n);
        minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
        maxX = Math.max(maxX, n.x + s.w); maxY = Math.max(maxY, n.y + s.h);
      });
      notes.forEach(function (n) {
        if (!n.el) return;
        minX = Math.min(minX, n.x); minY = Math.min(minY, n.y);
        maxX = Math.max(maxX, n.x + n.el.offsetWidth);
        maxY = Math.max(maxY, n.y + n.el.offsetHeight);
      });
      if (minX === Infinity) { minX = minY = 0; maxX = maxY = 100; }
      return { x: minX, y: minY, w: maxX - minX, h: maxY - minY };
    }

    function sizeWorld() {
      var b = contentBounds();
      var w = b.x + b.w + 200, h = b.y + b.h + 200;
      world.style.width = w + 'px';
      world.style.height = h + 'px';
      svg.setAttribute('width', w);
      svg.setAttribute('height', h);
      svgTop.setAttribute('width', w);
      svgTop.setAttribute('height', h);
    }

    // Regions stretch vertically to keep containing the cards whose center
    // falls in their column — expanding a card must not spill past its group.
    function growRegions() {
      regions.forEach(function (r) {
        if (!r.el) return;
        var h = r.h0 != null ? r.h0 : r.h;
        nodes.forEach(function (n) {
          if (!n.el) return;
          var cx = n.x + n.el.offsetWidth / 2;
          if (cx < r.x || cx > r.x + r.w) return;
          h = Math.max(h, n.y + n.el.offsetHeight + 40 - r.y);
        });
        r.h = h;
        r.el.style.height = h + 'px';
      });
    }

    function refresh() {
      growRegions();
      sizeWorld();
      drawEdges();
    }

    /* ---------- view control ---------- */

    function zoomAt(px, py, s) {
      s = clamp(s, 0.2, 2.5);
      var wx = (px - view.x) / view.s;
      var wy = (py - view.y) / view.s;
      view.s = s;
      view.x = px - wx * s;
      view.y = py - wy * s;
      applyView();
    }

    function zoomStep(factor) {
      var r = viewport.getBoundingClientRect();
      zoomAt(r.width / 2, r.height / 2, view.s * factor);
    }

    function fit(pad) {
      if (pad == null) pad = 50;
      var r = viewport.getBoundingClientRect();
      var b = contentBounds();
      var s = clamp(Math.min(
        (r.width - pad * 2) / b.w,
        (r.height - pad * 2) / b.h), 0.2, 1);
      view.s = s;
      view.x = (r.width - b.w * s) / 2 - b.x * s;
      view.y = (r.height - b.h * s) / 2 - b.y * s;
      applyView();
    }

    /* ---------- interaction ---------- */

    viewport.addEventListener('wheel', function (e) {
      e.preventDefault();
      var r = viewport.getBoundingClientRect();
      var f = Math.exp(-e.deltaY * 0.0016);
      zoomAt(e.clientX - r.left, e.clientY - r.top, view.s * f);
    }, { passive: false });

    viewport.addEventListener('pointerdown', function (e) {
      if (e.button !== 0) return;
      var header = e.target.closest('.gv-card-h');
      var card = e.target.closest('.gv-card');
      if (card && !header) return; // leave the JSON selectable

      var drag;
      if (header) {
        var node = nodes.find(function (n) { return n.el === card; });
        if (!node) return;
        drag = { node: node, sx: e.clientX, sy: e.clientY, ox: node.x, oy: node.y };
      } else {
        drag = { sx: e.clientX, sy: e.clientY, ox: view.x, oy: view.y };
        viewport.classList.add('panning');
      }
      viewport.setPointerCapture(e.pointerId);

      function move(ev) {
        var dx = ev.clientX - drag.sx, dy = ev.clientY - drag.sy;
        if (drag.node) {
          drag.node.x = drag.ox + dx / view.s;
          drag.node.y = drag.oy + dy / view.s;
          drag.node.el.style.left = drag.node.x + 'px';
          drag.node.el.style.top = drag.node.y + 'px';
          drawEdges();
        } else {
          view.x = drag.ox + dx;
          view.y = drag.oy + dy;
          applyView();
        }
      }
      function up(ev) {
        viewport.classList.remove('panning');
        viewport.removeEventListener('pointermove', move);
        viewport.removeEventListener('pointerup', up);
        viewport.removeEventListener('pointercancel', up);
        if (drag.node) refresh();
      }
      viewport.addEventListener('pointermove', move);
      viewport.addEventListener('pointerup', up);
      viewport.addEventListener('pointercancel', up);
    });

    viewport.addEventListener('click', function (e) {
      var btn = e.target.closest('.f-toggle');
      if (!btn) return;
      btn.closest('.gv-field').classList.toggle('open');
      refresh();
    });

    /* ---------- render ---------- */

    function render() {
      regions.forEach(function (r) {
        var d = el('div', 'gv-region', world);
        d.style.left = r.x + 'px';
        d.style.top = r.y + 'px';
        d.style.width = r.w + 'px';
        d.style.height = r.h + 'px';
        if (r.title) {
          var t = el('span', 'gv-region-title', d);
          t.textContent = r.title;
        }
        r.el = d;
        r.h0 = r.h;
      });

      notes.forEach(function (n) {
        var d = el('div', 'gv-worldnote', world);
        d.style.left = n.x + 'px';
        d.style.top = n.y + 'px';
        if (n.w) d.style.maxWidth = n.w + 'px';
        d.innerHTML = n.html;
        n.el = d;
      });

      nodes.forEach(function (n) {
        var card = el('div', 'gv-card tone-' + (n.tone || 'derived'), world);
        card.style.left = n.x + 'px';
        card.style.top = n.y + 'px';
        card.style.width = n.w + 'px';

        var h = el('div', 'gv-card-h', card);
        var title = el('span', 'gv-title', h);
        title.textContent = n.title;
        if (n.versioned) {
          el('span', 'gv-vtag', h).textContent = 'versioned';
        }
        if (n.badge) {
          var badge = el('span', 'gv-badge', h);
          badge.textContent = n.badge;
        }
        if (n.desc) {
          var desc = el('p', 'gv-desc', card);
          desc.textContent = n.desc;
        }
        if (n.schema) {
          renderFields(el('div', 'gv-schema', card), n.schema);
        } else if (n.json) {
          var pre = el('pre', 'gv-json', card);
          var code = el('code', '', pre);
          code.innerHTML = highlightJSON(n.json.trim());
        }
        if (n.note) {
          var note = el('div', 'gv-note', card);
          note.textContent = n.note;
        }
        n.el = card;
      });

      world.appendChild(svgTop);

      // Measure after layout (and again once fonts settle) before drawing.
      requestAnimationFrame(function () {
        refresh();
        fit();
      });
      if (document.fonts && document.fonts.ready) {
        document.fonts.ready.then(function () { drawEdges(); });
      }
    }

    return {
      addRegion: addRegion,
      addNote: addNote,
      addNode: addNode,
      addEdge: addEdge,
      render: render,
      fit: fit,
      zoomStep: zoomStep,
      redraw: drawEdges,
      refresh: refresh,
      onZoom: function (fn) { zoomListeners.push(fn); },
    };
  }

  // Draggable vertical divider between a sidebar and the rest of the row.
  function initSplitter(sidebar, handle) {
    handle.addEventListener('pointerdown', function (e) {
      e.preventDefault();
      handle.setPointerCapture(e.pointerId);
      handle.classList.add('active');
      var left = sidebar.getBoundingClientRect().left;
      function move(ev) {
        sidebar.style.width = clamp(ev.clientX - left, 140, 560) + 'px';
      }
      function up() {
        handle.classList.remove('active');
        handle.removeEventListener('pointermove', move);
        handle.removeEventListener('pointerup', up);
        handle.removeEventListener('pointercancel', up);
      }
      handle.addEventListener('pointermove', move);
      handle.addEventListener('pointerup', up);
      handle.addEventListener('pointercancel', up);
    });
  }

  window.GraphKit = { createGraph: createGraph, initSplitter: initSplitter };
})();
