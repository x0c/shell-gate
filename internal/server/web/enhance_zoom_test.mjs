import assert from 'node:assert/strict';
import fs from 'node:fs';
import vm from 'node:vm';

const source = fs.readFileSync(new URL('./enhance.js', import.meta.url), 'utf8');
function pick(name) {
  const start = source.indexOf('  function ' + name + '(');
  assert(start >= 0, `Missing ${name}`);
  return source.slice(start, source.indexOf('\n  }', start) + 4);
}
const sandbox = { window: { devicePixelRatio: 2 } };
vm.createContext(sandbox);
for (const name of ['unscaledTerminalPointer', 'installScaledTerminalPointer', 'tightFitStateKey']) {
  vm.runInContext(pick(name), sandbox);
}

// Match xterm 5.4's pixel-to-cell conversion with independent expected cells.
// Fractional layout dimensions must not be rounded to offsetWidth/offsetHeight.
const element = {
  style: { transform: 'scale(1.006, 1.03)', width: '1000.25px', height: '600.5px' },
  getBoundingClientRect: () => ({ left: 13, top: 27, width: 1006.25, height: 618.5 }),
};
const pointer = { clientX: 13 + 1006.25 * .975, clientY: 27 + 618.5 * .9375 };
const mouse = {
  getCoords(event, el, cols, rows, selection) {
    assert.equal(this, mouse);
    assert.equal(selection, true);
    assert.equal(cols, 100);
    assert.equal(rows, 40);
    return [Math.ceil((event.clientX - 13) / 10.0025 + .5), Math.ceil((event.clientY - 27) / 15.0125)];
  },
  getMouseReportCoords(event) {
    assert.equal(this, mouse);
    return { col: Math.floor((event.clientX - 13) / 10.0025), row: Math.floor((event.clientY - 27) / 15.0125) };
  },
};
const term = { _core: { _mouseService: mouse } };
sandbox.installScaledTerminalPointer(term);
const installed = mouse.getCoords;
sandbox.installScaledTerminalPointer(term);
assert.equal(mouse.getCoords, installed, 'Must not stack coordinate transforms');
assert.deepEqual(mouse.getMouseReportCoords(pointer, element), { col: 97, row: 37 });
assert.deepEqual(mouse.getCoords(pointer, element, 100, 40, true), [98, 38]);
assert.equal(pointer.clientX, 13 + 1006.25 * .975, 'Do not mutate native events');
assert.equal(sandbox.unscaledTerminalPointer(pointer, { style: {} }), pointer);
assert.equal(sandbox.unscaledTerminalPointer(pointer, { ...element, getBoundingClientRect: () => ({width:0,height:0}) }), pointer);
sandbox.installScaledTerminalPointer({});

const screen = { offsetWidth: 1000, offsetHeight: 600 };
term.element = { querySelector: () => screen };
term._core._renderService = { dimensions: { css: { cell: { width: 10, height: 15 } } } };
term.cols = 100; term.rows = 40;
const box = { getBoundingClientRect: () => ({ width: 1006, height: 618 }) };
const before = sandbox.tightFitStateKey(term, box);
sandbox.window.devicePixelRatio = 2.5;
assert.notEqual(sandbox.tightFitStateKey(term, box), before, 'DPR-only zoom invalidates fitting');
const after = sandbox.tightFitStateKey(term, box);
term._core._renderService.dimensions.css.cell.width += .001;
assert.notEqual(sandbox.tightFitStateKey(term, box), after, 'Subpixel renderer changes invalidate fitting');

let fitted = 0;
const timers = new Map();
const observed = [];
const queries = [];
sandbox.tightFitTerminal = () => { fitted++; };
sandbox.document = { getElementById: () => box };
sandbox.setTimeout = (fn, ms) => { timers.set(ms, fn); return ms; };
sandbox.clearTimeout = ms => timers.delete(ms);
sandbox.ResizeObserver = class {
  constructor(callback) { sandbox.resized = callback; }
  observe(el) { observed.push(el); }
};
sandbox.window.addEventListener = () => {};
sandbox.window.matchMedia = () => {
  const query = {
    addEventListener(name, fn) { this.listener = fn; },
    removeEventListener(name, fn) { assert.equal(this.listener, fn); this.listener = null; },
  };
  queries.push(query);
  return query;
};
vm.runInContext(pick('installTightFitOnResize'), sandbox);
sandbox.installTightFitOnResize(term);
assert.deepEqual(observed, [box, screen]);
assert.equal(fitted, 1);
sandbox.resized(); timers.get(50)();
assert.equal(fitted, 2, 'Renderer/container changes schedule refitting');
queries[0].listener(); timers.get(100)();
assert.equal(queries.length, 2, 'Re-arm the resolution query after zoom');
assert.equal(fitted, 3);
console.log('Terminal zoom: pointer mapping, selection, fractional dimensions, DPR and resize scheduling passed.');
