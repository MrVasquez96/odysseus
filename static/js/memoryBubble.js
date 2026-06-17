// static/js/memoryBubble.js
//
// Memory Bubble Visualization — force-directed graph of memories by category.
// Adapted from goWebCtrl's bubble-nav.js canvas engine.
// Self-contained, no external dependencies.

import uiModule from './ui.js';

const _DEFAULTS = {
    hubRadius:      44,
    categoryRadius: 34,
    leafRadius:     8,
    orbitRadius:    160,
    subOrbitRadius: 100,
    shrunkRadius:   16,
    transitionMs:   400,
    repulsion:      2200,
    springK:        0.15,
    centerK:        0.006,
    damping:        0.72,
    coolRate:       0.97,
    minAlpha:       0.001,
};

// Category colors — match common memory categories in Odysseus
const _CAT_COLORS = {
    fact:       '#3b82f6',
    identity:   '#8b5cf6',
    preference: '#f59e0b',
    contact:    '#ec4899',
    project:    '#10b981',
    goal:       '#06b6d4',
    task:       '#ef4444',
    general:    '#64748b',
};

function catColor(cat) {
    return _CAT_COLORS[cat] || _CAT_COLORS.general;
}

// ── State ────────────────────────────────────────────────────────

let _canvas = null;
let _ctx    = null;
let _nodes  = [];
let _links  = [];
let _nodeMap = {};
let _frame   = null;
let _alpha   = 0;
let _drag    = null;
let _hover   = null;
let _navStack = [];
let _trans    = null;
let _onLeafClick = null;
let _onDepthChange = null;
let _dragStart   = null;
let _dragHitNode = false;
let _eventsAttached = false;
let _destroyed = false;

// ── Public API ───────────────────────────────────────────────────

export function initBubbleView(canvasEl, opts) {
    _destroyed = false;
    _canvas = canvasEl;
    _ctx    = canvasEl.getContext('2d');
    _nodes  = [];
    _links  = [];
    _nodeMap = {};
    _navStack = [];
    _trans   = null;
    _hover   = null;
    _drag    = null;
    if (opts && opts.onLeafClick) _onLeafClick = opts.onLeafClick;
    if (opts && opts.onDepthChange) _onDepthChange = opts.onDepthChange;
    _attachEvents();
    _setupResize();
}

let _resizeObs = null;
let _lastW = 0, _lastH = 0;
function _setupResize() {
    if (!_canvas) return;
    _resizeObs = new ResizeObserver(() => {
        if (!_canvas || _destroyed) return;
        const container = _canvas.parentElement;
        if (!container) return;
        const w = container.clientWidth;
        const h = container.clientHeight - (_canvas.offsetTop - container.offsetTop);
        // Only resize if dimensions actually changed — prevents layout thrashing
        if (w === _lastW && h === _lastH) return;
        _lastW = w; _lastH = h;
        _canvas.width = w;
        _canvas.height = h;
        _wake(0);
    });
    _resizeObs.observe(_canvas.parentElement);
}

export function destroyBubbleView() {
    _destroyed = true;
    if (_frame) { cancelAnimationFrame(_frame); _frame = null; }
    if (_resizeObs) { _resizeObs.disconnect(); _resizeObs = null; }
    _lastW = 0; _lastH = 0;
    _eventsAttached = false;
    _canvas = null;
    _ctx = null;
}

export async function loadBubbleGraph() {
    try {
        const res = await fetch('/api/memory', { credentials: 'same-origin' });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const raw = await res.json();
        // Python returns {memory: [...]} or a plain array
        const memories = Array.isArray(raw) ? raw : (raw.memory || raw.memories || []);
        const data = _memoriesToGraph(memories);
        _buildGraph(data);
        return data;
    } catch (e) {
        console.error('Memory bubble: failed to load graph', e);
        throw e;
    }
}

// Build the {nodes, links} graph structure from the flat memory array
// returned by /api/memory — same data the browse list uses.
function _memoriesToGraph(memories) {
    const nodes = [];
    const links = [];

    // Central hub
    nodes.push({ id: '__hub__', label: 'MEMORIES', group: 'hub', size: 50 });

    // Group by category
    const groups = {};
    for (const m of memories) {
        const cat = (m.category || 'fact').toLowerCase().trim() || 'fact';
        if (!groups[cat]) groups[cat] = [];
        groups[cat].push(m);
    }

    for (const [cat, mems] of Object.entries(groups)) {
        const grpId = 'mem_grp:' + cat;
        const sz = Math.min(60, Math.max(32, 26 + mems.length * 3));
        nodes.push({ id: grpId, label: cat.toUpperCase(), group: 'mem_category', size: sz });
        links.push({ source: '__hub__', target: grpId });

        for (const m of mems) {
            const text = m.text || '';
            const label = text.length > 40 ? text.slice(0, 39) + '\u2026' : text;
            const content = text.length > 200 ? text.slice(0, 197) + '...' : text;
            nodes.push({
                id: 'mem:' + m.id,
                label,
                group: 'mem_leaf',
                content,
                updatedAt: m.updated_at || '',
                size: 7,
            });
            links.push({ source: grpId, target: 'mem:' + m.id });
        }
    }

    return { nodes, links };
}

export function bubbleBack() {
    if (_navStack.length === 0) return;
    _navStack.pop();
    if (_navStack.length === 0) {
        _transitionToHome();
    } else {
        _transitionToLevel();
    }
}

export function getBubbleDepth() {
    return _navStack.length;
}

// ── Graph Building ───────────────────────────────────────────────

function _buildGraph(data) {
    _nodes = [];
    _links = [];
    _nodeMap = {};

    const rawNodes = data.nodes || [];
    const rawLinks = data.links || [];

    for (const rn of rawNodes) {
        const n = {
            id:      rn.id,
            label:   rn.label,
            group:   rn.group,
            content: rn.content || '',
            updatedAt: rn.updatedAt || '',
            color:   _resolveColor(rn),
            type:    rn.group === 'hub' ? 'hub' : rn.group === 'mem_category' ? 'category' : 'leaf',
            parentId: null,
            x: 0, y: 0, vx: 0, vy: 0,
            fx: null, fy: null,
            radius: 0, targetR: 0,
            visible: true,
            description: '',
        };
        _nodes.push(n);
        _nodeMap[n.id] = n;
    }

    for (const rl of rawLinks) {
        _links.push({ source: rl.source, target: rl.target, distance: _DEFAULTS.orbitRadius });
        // Set parent
        const child = _nodeMap[rl.target];
        if (child) child.parentId = rl.source;
    }

    // Set descriptions on categories
    for (const n of _nodes) {
        if (n.type === 'category') {
            const kids = _nodes.filter(c => c.parentId === n.id);
            n.description = kids.length + (kids.length === 1 ? ' memory' : ' memories');
        }
    }

    _initPositions();
    _startSim();
}

function _resolveColor(rn) {
    if (rn.group === 'hub') return '#94a3b8';
    if (rn.group === 'mem_category') {
        const cat = rn.id.replace('mem_grp:', '');
        return catColor(cat);
    }
    // Leaf: inherit parent category color
    return '#64748b';
}

// ── Positions ────────────────────────────────────────────────────

function _initPositions() {
    if (!_canvas) return;
    const w = _canvas.offsetWidth || 600;
    const h = _canvas.offsetHeight || 400;
    const cx = w / 2, cy = h / 2;

    const categories = _nodes.filter(n => n.type === 'category');
    const count = categories.length;

    for (const n of _nodes) {
        if (n.type === 'hub') {
            n.x = cx; n.y = cy;
            n.radius = _DEFAULTS.hubRadius;
            n.targetR = n.radius;
        } else if (n.type === 'category') {
            const idx = categories.indexOf(n);
            const angle = (idx / count) * Math.PI * 2 - Math.PI / 2;
            n.x = cx + Math.cos(angle) * _DEFAULTS.orbitRadius;
            n.y = cy + Math.sin(angle) * _DEFAULTS.orbitRadius;
            n.radius = _DEFAULTS.categoryRadius;
            n.targetR = n.radius;
        } else {
            const parent = _nodeMap[n.parentId];
            n.x = parent ? parent.x : cx;
            n.y = parent ? parent.y : cy;
            n.radius = 0;
            n.targetR = 0;
            n.visible = false;
        }
        n.vx = 0; n.vy = 0;
        n.fx = null; n.fy = null;
        if (n.visible === undefined) n.visible = true;
    }
}

// ── Simulation ───────────────────────────────────────────────────

function _startSim() {
    _alpha = 1.0;
    if (_frame) cancelAnimationFrame(_frame);

    function tick() {
        if (_destroyed) return;
        if (_alpha > _DEFAULTS.minAlpha) {
            _simStep(_alpha);
            _alpha *= _DEFAULTS.coolRate;
        }
        _updateTransition();
        _draw();
        _frame = requestAnimationFrame(tick);
    }
    tick();
}

function _wake(a) { if (_alpha < a) _alpha = a; }

function _simStep(alpha) {
    if (!_canvas) return;
    const w = _canvas.offsetWidth || 600;
    const h = _canvas.offsetHeight || 400;
    const cx = w / 2, cy = h / 2;
    const visible = _nodes.filter(n => n.visible !== false && n.radius > 0.5);

    // Repulsion
    for (let i = 0; i < visible.length; i++) {
        for (let j = i + 1; j < visible.length; j++) {
            const a = visible[i], b = visible[j];
            let dx = b.x - a.x, dy = b.y - a.y;
            const d2 = dx * dx + dy * dy + 0.1;
            const d = Math.sqrt(d2);
            const f = (_DEFAULTS.repulsion / d2) * alpha;
            dx /= d; dy /= d;
            if (a.fx === null) { a.vx -= dx * f; a.vy -= dy * f; }
            if (b.fx === null) { b.vx += dx * f; b.vy += dy * f; }
        }
    }

    // Springs
    for (const lnk of _links) {
        const s = _nodeMap[lnk.source], t = _nodeMap[lnk.target];
        if (!s || !t || s.visible === false || t.visible === false) continue;
        if (s.radius < 0.5 || t.radius < 0.5) continue;
        const dx = t.x - s.x, dy = t.y - s.y;
        const d = Math.sqrt(dx * dx + dy * dy) + 0.1;
        const ideal = lnk.distance || _DEFAULTS.orbitRadius;
        const f = (d - ideal) * _DEFAULTS.springK * alpha;
        const fx = (dx / d) * f, fy = (dy / d) * f;
        if (s.fx === null) { s.vx += fx; s.vy += fy; }
        if (t.fx === null) { t.vx -= fx; t.vy -= fy; }
    }

    // Centering
    for (const n of visible) {
        if (n.fx !== null) continue;
        n.vx += (cx - n.x) * _DEFAULTS.centerK * alpha;
        n.vy += (cy - n.y) * _DEFAULTS.centerK * alpha;
    }

    // Integrate
    for (const n of visible) {
        if (n.fx !== null) { n.x = n.fx; n.y = n.fy; continue; }
        n.vx *= _DEFAULTS.damping;
        n.vy *= _DEFAULTS.damping;
        n.x += n.vx; n.y += n.vy;
        const r = (n.radius || 8) + 4;
        n.x = Math.max(r, Math.min(w - r, n.x));
        n.y = Math.max(r, Math.min(h - r, n.y));
    }
}

// ── Transitions ──────────────────────────────────────────────────

function _transitionToLevel() {
    if (!_canvas) return;
    const w = _canvas.offsetWidth || 600;
    const h = _canvas.offsetHeight || 400;
    const cx = w / 2, cy = h / 2;

    const currentId = _navStack[_navStack.length - 1];
    const current = _nodeMap[currentId];
    if (!current) return;

    const children = _nodes.filter(n => n.parentId === currentId);
    const expandedR = Math.min(w, h) * 0.25;
    const shrunkOrbit = Math.max(w, h) * 0.42;

    const targets = {};

    // Hide all by default
    for (const n of _nodes) {
        targets[n.id] = { targetR: 0, visible: false };
    }

    // Expanded node center
    targets[currentId] = { targetX: cx, targetY: cy, targetR: expandedR, visible: true };

    // Siblings
    const siblings = _getSiblings(currentId);
    for (let i = 0; i < siblings.length; i++) {
        const angle = (i / siblings.length) * Math.PI * 2 - Math.PI / 2;
        targets[siblings[i].id] = {
            targetX: cx + Math.cos(angle) * shrunkOrbit,
            targetY: cy + Math.sin(angle) * shrunkOrbit,
            targetR: _DEFAULTS.shrunkRadius,
            visible: true,
        };
    }

    // Hub in corner
    targets['__hub__'] = {
        targetX: 40, targetY: 30,
        targetR: _DEFAULTS.shrunkRadius,
        visible: true,
    };

    // Children inside expanded area
    if (children.length > 0) {
        for (let i = 0; i < children.length; i++) {
            const angle = (i / children.length) * Math.PI * 2 - Math.PI / 2;
            const subOrbit = expandedR * 0.55;
            targets[children[i].id] = {
                targetX: cx + Math.cos(angle) * subOrbit,
                targetY: cy + Math.sin(angle) * subOrbit,
                targetR: _DEFAULTS.leafRadius + 4,
                visible: true,
            };
        }
    }

    _startTransition(targets);
    if (_onDepthChange) _onDepthChange(_navStack.length);
}

function _transitionToHome() {
    if (!_canvas) return;
    const w = _canvas.offsetWidth || 600;
    const h = _canvas.offsetHeight || 400;
    const cx = w / 2, cy = h / 2;

    const categories = _nodes.filter(n => n.type === 'category');
    const count = categories.length;
    const targets = {};

    targets['__hub__'] = { targetX: cx, targetY: cy, targetR: _DEFAULTS.hubRadius, visible: true };

    for (let i = 0; i < count; i++) {
        const n = categories[i];
        const angle = (i / count) * Math.PI * 2 - Math.PI / 2;
        targets[n.id] = {
            targetX: cx + Math.cos(angle) * _DEFAULTS.orbitRadius,
            targetY: cy + Math.sin(angle) * _DEFAULTS.orbitRadius,
            targetR: _DEFAULTS.categoryRadius,
            visible: true,
        };
    }

    // Hide leaves
    for (const n of _nodes) {
        if (n.type === 'leaf') targets[n.id] = { targetR: 0, visible: false };
    }

    _startTransition(targets);
    if (_onDepthChange) _onDepthChange(_navStack.length);
}

function _getSiblings(nodeId) {
    const node = _nodeMap[nodeId];
    if (!node) return [];
    if (node.type === 'category') {
        return _nodes.filter(n => n.type === 'category' && n.id !== nodeId);
    }
    if (node.type === 'leaf') {
        return _nodes.filter(n => n.type === 'leaf' && n.parentId === node.parentId && n.id !== nodeId);
    }
    return [];
}

function _startTransition(targets) {
    const from = {};
    for (const n of _nodes) {
        from[n.id] = { x: n.x, y: n.y, r: n.radius, visible: n.visible !== false };
    }
    _trans = { start: performance.now(), duration: _DEFAULTS.transitionMs, from, to: targets };
    _wake(0.5);
}

function _updateTransition() {
    if (!_trans) return;
    const elapsed = performance.now() - _trans.start;
    const raw = Math.min(1, elapsed / _trans.duration);
    const t = 1 - Math.pow(1 - raw, 3);

    for (const n of _nodes) {
        const f = _trans.from[n.id];
        const to = _trans.to[n.id];
        if (!f) continue;
        if (to) {
            if (to.targetX !== undefined) {
                n.x = f.x + (to.targetX - f.x) * t;
                n.y = f.y + (to.targetY - f.y) * t;
                n.fx = n.x; n.fy = n.y;
            }
            if (to.targetR !== undefined) {
                n.radius = f.r + (to.targetR - f.r) * t;
            }
            if (to.visible !== undefined) {
                n.visible = to.visible || n.radius > 0.5;
            }
        }
    }

    if (raw >= 1) {
        for (const n of _nodes) {
            const to = _trans.to[n.id];
            if (to) {
                if (to.targetX !== undefined) { n.x = to.targetX; n.y = to.targetY; }
                if (to.targetR !== undefined) n.radius = to.targetR;
                if (to.visible !== undefined) n.visible = to.visible;
            }
            if (n.radius > _DEFAULTS.shrunkRadius + 1) {
                n.fx = n.x; n.fy = n.y;
            } else if (n.visible) {
                n.fx = null; n.fy = null;
            }
        }
        _trans = null;
    }
}

// ── Rendering ────────────────────────────────────────────────────

function _draw() {
    if (!_canvas || !_ctx) return;
    const ctx = _ctx;
    const dpr = window.devicePixelRatio || 1;
    const w = _canvas.offsetWidth || 600;
    const h = _canvas.offsetHeight || 400;

    if (_canvas.width !== Math.round(w * dpr) || _canvas.height !== Math.round(h * dpr)) {
        _canvas.width  = Math.round(w * dpr);
        _canvas.height = Math.round(h * dpr);
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }

    const bg = getComputedStyle(document.documentElement).getPropertyValue('--panel').trim() || '#1e1e2e';
    ctx.fillStyle = bg;
    ctx.fillRect(0, 0, w, h);

    // Links
    for (const lnk of _links) {
        const s = _nodeMap[lnk.source], t = _nodeMap[lnk.target];
        if (!s || !t || s.visible === false || t.visible === false) continue;
        if (s.radius < 1 || t.radius < 1) continue;
        const opacity = Math.min(s.radius, t.radius) / _DEFAULTS.categoryRadius;
        ctx.beginPath();
        ctx.strokeStyle = `rgba(148,163,184,${0.2 * Math.min(1, opacity)})`;
        ctx.lineWidth = Math.max(0.5, 1.2 * Math.min(1, opacity));
        ctx.moveTo(s.x, s.y);
        ctx.lineTo(t.x, t.y);
        ctx.stroke();
    }

    // Nodes
    for (const n of _nodes) {
        if (n.visible === false || n.radius < 0.5) continue;
        _drawNode(ctx, n);
    }
}

function _drawNode(ctx, n) {
    const r = n.radius;
    const col = n.color || '#64748b';

    // Glow
    if (r > 10) {
        const grad = ctx.createRadialGradient(n.x, n.y, r * 0.3, n.x, n.y, r * 1.8);
        grad.addColorStop(0, col + '25');
        grad.addColorStop(1, col + '00');
        ctx.beginPath();
        ctx.arc(n.x, n.y, r * 1.8, 0, Math.PI * 2);
        ctx.fillStyle = grad;
        ctx.fill();
    }

    // Circle
    ctx.beginPath();
    ctx.arc(n.x, n.y, r, 0, Math.PI * 2);
    ctx.fillStyle = col + (n.type === 'hub' ? '44' : '22');
    ctx.fill();

    // Border
    ctx.strokeStyle = col;
    ctx.lineWidth = n.type === 'hub' ? 2.5 : r > 20 ? 2 : 1.5;
    ctx.stroke();

    // Hover ring
    if (_hover && _hover.id === n.id && !_trans) {
        ctx.beginPath();
        ctx.arc(n.x, n.y, r + 3, 0, Math.PI * 2);
        ctx.strokeStyle = col + '88';
        ctx.lineWidth = 1.5;
        ctx.setLineDash([4, 3]);
        ctx.stroke();
        ctx.setLineDash([]);
    }

    // Label
    if (r >= 12) {
        const maxCh = Math.max(3, Math.floor(r / 4.2));
        let label = n.label || '';
        if (label.length > maxCh) label = label.slice(0, maxCh - 1) + '\u2026';
        const fs = Math.max(8, Math.min(13, Math.floor(r * 0.3)));
        ctx.font = 'bold ' + fs + 'px monospace';
        ctx.fillStyle = '#f1f5f9';
        ctx.textAlign = 'center';
        ctx.textBaseline = 'middle';
        ctx.fillText(label.toUpperCase(), n.x, n.y);
    }
}

// ── Hit Detection ────────────────────────────────────────────────

function _nodeAt(clientX, clientY) {
    if (!_canvas) return null;
    const rect = _canvas.getBoundingClientRect();
    const x = clientX - rect.left, y = clientY - rect.top;
    for (let i = _nodes.length - 1; i >= 0; i--) {
        const n = _nodes[i];
        if (n.visible === false || n.radius < 2) continue;
        const hitR = n.radius + 6;
        const dx = n.x - x, dy = n.y - y;
        if (dx * dx + dy * dy <= hitR * hitR) return n;
    }
    return null;
}

// ── Events ───────────────────────────────────────────────────────

function _attachEvents() {
    if (_eventsAttached || !_canvas) return;
    _eventsAttached = true;

    _canvas.addEventListener('mousedown', e => {
        const n = _nodeAt(e.clientX, e.clientY);
        _dragStart = { x: e.clientX, y: e.clientY };
        _dragHitNode = !!n;
        if (n && n.radius > 5) {
            _drag = n;
            n.fx = n.x; n.fy = n.y;
            _canvas.style.cursor = 'grabbing';
            _wake(0.3);
        }
    });

    _canvas.addEventListener('mousemove', e => {
        if (_drag) {
            const rect = _canvas.getBoundingClientRect();
            _drag.fx = e.clientX - rect.left;
            _drag.fy = e.clientY - rect.top;
            _drag.x = _drag.fx;
            _drag.y = _drag.fy;
            return;
        }
        const n = _nodeAt(e.clientX, e.clientY);
        _hover = n;
        _canvas.style.cursor = n ? 'pointer' : 'default';
    });

    _canvas.addEventListener('mouseup', e => {
        const wasDrag = _dragStart &&
            (Math.abs(e.clientX - _dragStart.x) > 5 || Math.abs(e.clientY - _dragStart.y) > 5);

        if (_drag) {
            if (_navStack.length === 0) { _drag.fx = null; _drag.fy = null; }
            if (!wasDrag) _handleClick(_drag);
            _drag = null;
        } else if (!wasDrag && !_dragHitNode && _navStack.length > 0) {
            if (e.target === _canvas) bubbleBack();
        }

        _dragStart = null;
        _dragHitNode = false;
        if (_canvas) _canvas.style.cursor = 'default';
    });

    _canvas.addEventListener('mouseleave', () => { _hover = null; });

    // Touch
    _canvas.addEventListener('touchstart', e => {
        if (e.touches.length > 1) return;
        const t = e.changedTouches[0];
        _canvas.dispatchEvent(new MouseEvent('mousedown', { clientX: t.clientX, clientY: t.clientY, bubbles: true }));
        e.preventDefault();
    }, { passive: false });
    _canvas.addEventListener('touchmove', e => {
        if (e.touches.length > 1) return;
        const t = e.changedTouches[0];
        _canvas.dispatchEvent(new MouseEvent('mousemove', { clientX: t.clientX, clientY: t.clientY, bubbles: true }));
    }, { passive: true });
    _canvas.addEventListener('touchend', e => {
        const t = e.changedTouches[0];
        _canvas.dispatchEvent(new MouseEvent('mouseup', { clientX: t.clientX, clientY: t.clientY, bubbles: true }));
        e.preventDefault();
    }, { passive: false });
}

function _handleClick(node) {
    if (_trans) return;

    if (node.type === 'hub') {
        if (_navStack.length > 0) {
            _navStack = [];
            _transitionToHome();
        }
        return;
    }

    // Clicking current expanded node — no action
    if (_navStack.length > 0 && _navStack[_navStack.length - 1] === node.id) return;

    // Sibling swap
    if (_navStack.length > 0) {
        const currentId = _navStack[_navStack.length - 1];
        const siblings = _getSiblings(currentId);
        if (siblings.some(s => s.id === node.id)) {
            _navStack[_navStack.length - 1] = node.id;
            _transitionToLevel();
            return;
        }
        // Child
        const children = _nodes.filter(n => n.parentId === currentId);
        if (children.some(c => c.id === node.id)) {
            if (node.type === 'leaf' && _onLeafClick) {
                _onLeafClick(node);
                return;
            }
            _navStack.push(node.id);
            _transitionToLevel();
            return;
        }
    }

    // Home level click
    if (_navStack.length === 0 && node.type === 'category') {
        _navStack.push(node.id);
        _transitionToLevel();
        return;
    }

    // Leaf click at any level
    if (node.type === 'leaf' && _onLeafClick) {
        _onLeafClick(node);
    }
}

const memoryBubbleModule = {
    initBubbleView,
    destroyBubbleView,
    loadBubbleGraph,
    bubbleBack,
    getBubbleDepth,
};

export default memoryBubbleModule;
