// Cledyu Redesign v2 디자인 소스(particle-fx.js)의 캔버스 애니메이션을 커스텀 엘리먼트로 그대로 포팅.
// seed 객체 모양이 kind별로 달라 엄격 타입 부여 실익이 낮음.
// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-nocheck
'use client';

import { useEffect } from 'react';

type ParticleKind = 'stars' | 'rack' | 'desk' | 'check';

let registered = false;

function registerParticleFx() {
  if (registered || typeof window === 'undefined') return;
  registered = true;
  if (customElements.get('particle-fx')) return;

  function rand(a, b) {
    return a + Math.random() * (b - a);
  }

  class ParticleFxElement extends HTMLElement {
    connectedCallback() {
      if (this._canvas) return;
      this.style.display = 'block';
      const canvas = document.createElement('canvas');
      canvas.style.cssText = 'width:100%;height:100%;display:block;';
      this.appendChild(canvas);
      this._canvas = canvas;
      const ctx = canvas.getContext('2d');
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      const resize = () => {
        const r = this.getBoundingClientRect();
        canvas.width = Math.max(2, Math.round(r.width * dpr));
        canvas.height = Math.max(2, Math.round(r.height * dpr));
      };
      this._ro = new ResizeObserver(resize);
      this._ro.observe(this);
      resize();

      const kind = this.getAttribute('kind') || 'rack';
      let seeded = null;
      let t = 0;

      const loop = () => {
        this._raf = requestAnimationFrame(loop);
        t += 0.016;
        const w = canvas.width / dpr,
          h = canvas.height / dpr;
        if (w < 4 || h < 4) return;
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
        ctx.clearRect(0, 0, w, h);
        ctx.fillStyle = '#fff';

        if (kind === 'stars') {
          if (!seeded) {
            const stars = [];
            for (let i = 0; i < 420; i++)
              stars.push({
                x: Math.random(),
                y: Math.random(),
                al: rand(0.1, 0.65),
                sz: Math.random() < 0.06 ? rand(1.2, 2) : rand(0.4, 1.1),
                tw: rand(0.4, 2.2),
                ph: rand(0, 6),
                dr: rand(0.002, 0.01),
              });
            const glows = [];
            for (let i = 0; i < 4; i++)
              glows.push({
                x: Math.random(),
                y: Math.random(),
                r: rand(0.18, 0.34),
                al: rand(0.012, 0.025),
                sp: rand(0.01, 0.03),
                ph: rand(0, 6),
              });
            seeded = { stars, glows };
          }
          for (const g of seeded.glows) {
            const gx = ((g.x + Math.sin(t * g.sp + g.ph) * 0.02) % 1) * w;
            const gy = g.y * h;
            const gr = g.r * Math.max(w, h);
            const grad = ctx.createRadialGradient(gx, gy, 0, gx, gy, gr);
            grad.addColorStop(0, 'rgba(255,255,255,' + g.al + ')');
            grad.addColorStop(1, 'rgba(255,255,255,0)');
            ctx.fillStyle = grad;
            ctx.fillRect(gx - gr, gy - gr, gr * 2, gr * 2);
          }
          ctx.fillStyle = '#fff';
          for (const s of seeded.stars) {
            s.x += s.dr * 0.016;
            if (s.x > 1.01) s.x = -0.01;
            ctx.globalAlpha = s.al * (0.45 + 0.55 * Math.sin(t * s.tw + s.ph));
            ctx.fillRect(s.x * w, s.y * h, s.sz, s.sz);
          }
          ctx.globalAlpha = 1;
        } else if (kind === 'rack') {
          const S = Math.min(w, h) * 1.05;
          const cx = w / 2,
            cy = h / 2;
          if (!seeded) {
            const dust = [];
            for (let i = 0; i < 260; i++)
              dust.push({
                x: Math.random(),
                y: Math.random(),
                al: rand(0.04, 0.28),
                sz: rand(0.4, 1.1),
                dr: rand(0.3, 1),
              });
            const leds = [];
            for (let i = 0; i < 8; i++)
              leds.push({ p1: rand(0, 6), p2: rand(0, 6), s1: rand(1.5, 4), s2: rand(1.5, 4) });
            const packets = [0, 1, 2].map((i) => ({
              vm: i,
              p: Math.random(),
              sp: rand(0.18, 0.3),
            }));
            seeded = { dust, leds, packets };
          }
          for (const d of seeded.dust) {
            ctx.globalAlpha = d.al * (0.5 + 0.5 * Math.sin(t * d.dr + d.x * 9));
            ctx.fillRect(d.x * w, d.y * h, d.sz, d.sz);
          }
          ctx.globalAlpha = 1;
          ctx.strokeStyle = 'rgba(255,255,255,0.85)';
          ctx.lineWidth = 1.4;
          const rw = S * 0.3,
            rh = S * 0.68,
            rx = cx - S * 0.48,
            ry = cy - rh / 2;
          ctx.save();
          ctx.shadowColor = 'rgba(255,255,255,0.35)';
          ctx.shadowBlur = 10;
          ctx.strokeRect(rx, ry, rw, rh);
          ctx.restore();
          const units = 8,
            uh = rh / units;
          for (let i = 1; i < units; i++) {
            ctx.globalAlpha = 0.5;
            ctx.beginPath();
            ctx.moveTo(rx, ry + i * uh);
            ctx.lineTo(rx + rw, ry + i * uh);
            ctx.stroke();
          }
          ctx.globalAlpha = 1;
          for (let i = 0; i < units; i++) {
            const uy = ry + i * uh + uh / 2;
            ctx.globalAlpha = 0.55;
            ctx.beginPath();
            ctx.moveTo(rx + rw * 0.1, uy);
            ctx.lineTo(rx + rw * 0.52, uy);
            ctx.stroke();
            ctx.globalAlpha = 1;
            const led = seeded.leds[i];
            const on1 = Math.sin(t * led.s1 + led.p1) > 0;
            const on2 = Math.sin(t * led.s2 + led.p2) > 0.4;
            ctx.globalAlpha = on1 ? 0.95 : 0.15;
            ctx.beginPath();
            ctx.arc(rx + rw * 0.72, uy, 2.2, 0, Math.PI * 2);
            ctx.fill();
            ctx.globalAlpha = on2 ? 0.95 : 0.15;
            ctx.beginPath();
            ctx.arc(rx + rw * 0.86, uy, 2.2, 0, Math.PI * 2);
            ctx.fill();
            ctx.globalAlpha = 1;
          }
          const vw = S * 0.17,
            vh = S * 0.11;
          const vms = [-1, 0, 1].map((k) => ({ x: cx + S * 0.3, y: cy + k * S * 0.2 - vh / 2 }));
          const outX = rx + rw,
            outY = cy;
          for (let i = 0; i < 3; i++) {
            const v = vms[i];
            ctx.globalAlpha = 0.4;
            ctx.beginPath();
            ctx.moveTo(outX, outY);
            ctx.bezierCurveTo(
              outX + S * 0.14,
              outY,
              v.x - S * 0.14,
              v.y + vh / 2,
              v.x,
              v.y + vh / 2,
            );
            ctx.stroke();
            ctx.globalAlpha = 1;
            const pulse = 0.65 + 0.35 * Math.sin(t * 1.5 + i * 2.1);
            ctx.save();
            ctx.shadowColor = 'rgba(255,255,255,0.5)';
            ctx.shadowBlur = 8 * pulse;
            ctx.strokeRect(v.x, v.y, vw, vh);
            ctx.restore();
            ctx.globalAlpha = 0.7;
            ctx.beginPath();
            ctx.moveTo(v.x + vw * 0.12, v.y + vh * 0.38);
            ctx.lineTo(v.x + vw * 0.32, v.y + vh * 0.38);
            ctx.stroke();
            if (Math.sin(t * 4 + i) > 0)
              ctx.fillRect(v.x + vw * 0.36, v.y + vh * 0.28, 3, vh * 0.2);
            ctx.globalAlpha = 1;
          }
          for (const pk of seeded.packets) {
            pk.p += pk.sp * 0.016;
            if (pk.p > 1) pk.p = 0;
            const v = vms[pk.vm];
            const tt = pk.p;
            const x0 = outX,
              y0 = outY,
              x3 = v.x,
              y3 = v.y + vh / 2;
            const x1 = outX + S * 0.14,
              y1 = outY,
              x2 = v.x - S * 0.14,
              y2 = y3;
            const u = 1 - tt;
            const bx =
              u * u * u * x0 + 3 * u * u * tt * x1 + 3 * u * tt * tt * x2 + tt * tt * tt * x3;
            const by =
              u * u * u * y0 + 3 * u * u * tt * y1 + 3 * u * tt * tt * y2 + tt * tt * tt * y3;
            ctx.save();
            ctx.shadowColor = 'rgba(255,255,255,0.9)';
            ctx.shadowBlur = 7;
            ctx.beginPath();
            ctx.arc(bx, by, 1.8, 0, Math.PI * 2);
            ctx.fill();
            ctx.restore();
          }
        } else if (kind === 'desk') {
          const S = Math.min(w, h) * 1.15;
          const cx = w / 2,
            base = h * 0.78;
          if (!seeded) {
            const dust = [];
            for (let i = 0; i < 200; i++)
              dust.push({
                x: Math.random(),
                y: Math.random(),
                al: rand(0.04, 0.25),
                sz: rand(0.4, 1),
                dr: rand(0.3, 1),
              });
            seeded = {
              dust,
              term: {
                script: [
                  { cmd: 'ls /var/www', out: ['app  deploy.sh  index.html'] },
                  { cmd: 'chmod +x deploy.sh', out: [] },
                  { cmd: './deploy.sh', out: ['deploying...', 'service is up'] },
                  { cmd: 'kubectl get pods', out: ['web-7d4f   1/1   Running'] },
                ],
                lines: [],
                cmdIdx: 0,
                chars: 0,
                outIdx: 0,
                phase: 'type',
                wait: 0.8,
                last: 0,
              },
            };
          }
          for (const d of seeded.dust) {
            ctx.globalAlpha = d.al * (0.5 + 0.5 * Math.sin(t * d.dr + d.x * 9));
            ctx.fillRect(d.x * w, d.y * h, d.sz, d.sz);
          }
          ctx.globalAlpha = 1;
          ctx.strokeStyle = 'rgba(255,255,255,0.85)';
          ctx.lineWidth = 1.6;
          ctx.lineCap = 'round';
          ctx.beginPath();
          ctx.moveTo(cx - S * 0.52, base);
          ctx.lineTo(cx + S * 0.52, base);
          ctx.stroke();
          const mw = S * 0.4,
            mh = S * 0.27,
            mx = cx - S * 0.46,
            my = base - S * 0.35;
          ctx.save();
          ctx.shadowColor = 'rgba(255,255,255,0.5)';
          ctx.shadowBlur = 10;
          ctx.strokeRect(mx, my, mw, mh);
          ctx.restore();
          ctx.beginPath();
          ctx.moveTo(mx + mw / 2, my + mh);
          ctx.lineTo(mx + mw / 2, base - S * 0.02);
          ctx.moveTo(mx + mw / 2 - S * 0.05, base);
          ctx.lineTo(mx + mw / 2 + S * 0.05, base);
          ctx.stroke();
          const term = seeded.term;
          const dt2 = Math.min(0.1, t - term.last);
          term.last = t;
          term.wait -= dt2;
          if (term.wait <= 0) {
            const cur = term.script[term.cmdIdx];
            if (term.phase === 'type') {
              if (term.chars < cur.cmd.length) {
                term.chars++;
                term.wait = 0.05 + Math.random() * 0.09;
              } else {
                term.phase = 'enter';
                term.wait = 0.5;
              }
            } else if (term.phase === 'enter') {
              term.lines.push('cmd:' + cur.cmd);
              term.phase = 'out';
              term.outIdx = 0;
              term.wait = 0.3;
            } else {
              if (term.outIdx < cur.out.length) {
                term.lines.push(cur.out[term.outIdx]);
                term.outIdx++;
                term.wait = 0.35;
              } else {
                term.cmdIdx = (term.cmdIdx + 1) % term.script.length;
                term.chars = 0;
                term.phase = 'type';
                term.wait = 1.0;
                if (term.cmdIdx === 0) {
                  term.lines = [];
                  term.wait = 1.6;
                }
              }
            }
          }
          const pad = mw * 0.08;
          const fs = Math.max(9, mw * 0.045);
          const lineH = fs * 1.7;
          const maxRows = Math.floor((mh - pad * 2) / lineH) - 1;
          const visible = term.lines.slice(-Math.max(1, maxRows));
          ctx.font = fs + "px 'JetBrains Mono', monospace";
          ctx.textBaseline = 'top';
          const DOLLAR = String.fromCharCode(36);
          for (let i = 0; i < visible.length; i++) {
            const ln = visible[i];
            const isCmd = ln.indexOf('cmd:') === 0;
            ctx.globalAlpha = isCmd ? 0.95 : 0.55;
            ctx.fillText(isCmd ? DOLLAR + ' ' + ln.slice(4) : ln, mx + pad, my + pad + i * lineH);
          }
          const typing = term.phase === 'type' || term.phase === 'enter';
          const typed = typing ? term.script[term.cmdIdx].cmd.slice(0, term.chars) : '';
          const py = my + pad + visible.length * lineH;
          if (py < my + mh - pad) {
            ctx.globalAlpha = 0.95;
            const promptStr = DOLLAR + ' ' + typed;
            ctx.fillText(promptStr, mx + pad, py);
            if (Math.sin(t * 6) > -0.2) {
              const tw = ctx.measureText(promptStr).width;
              ctx.fillRect(mx + pad + tw + 2, py, fs * 0.5, fs);
            }
          }
          ctx.globalAlpha = 1;
          ctx.textBaseline = 'alphabetic';
          const bob = Math.sin(t * 0.9) * S * 0.006;
          const hx = cx + S * 0.24,
            hy = base - S * 0.3 + bob,
            hr = S * 0.045;
          ctx.save();
          ctx.shadowColor = 'rgba(255,255,255,0.4)';
          ctx.shadowBlur = 8;
          ctx.beginPath();
          ctx.arc(hx, hy, hr, 0, Math.PI * 2);
          ctx.stroke();
          ctx.restore();
          ctx.beginPath();
          ctx.moveTo(hx + hr * 0.5, hy + hr);
          ctx.quadraticCurveTo(hx + S * 0.09, base - S * 0.16, hx + S * 0.075, base);
          ctx.stroke();
          const ty = Math.sin(t * 7) * 1.6;
          ctx.beginPath();
          ctx.moveTo(hx + hr * 0.2, hy + hr * 1.6);
          ctx.quadraticCurveTo(hx - S * 0.05, base - S * 0.1, hx - S * 0.135, base - S * 0.02 + ty);
          ctx.stroke();
          ctx.beginPath();
          ctx.moveTo(hx - S * 0.2, base - S * 0.012);
          ctx.lineTo(hx - S * 0.09, base - S * 0.012);
          ctx.stroke();
          ctx.globalAlpha = 0.6;
          ctx.beginPath();
          ctx.moveTo(hx + S * 0.115, base - S * 0.06);
          ctx.lineTo(hx + S * 0.115, base + S * 0.001);
          ctx.moveTo(hx + S * 0.03, base);
          ctx.lineTo(hx + S * 0.16, base);
          ctx.stroke();
          ctx.globalAlpha = 1;
        } else if (kind === 'check') {
          const S = Math.min(w, h);
          const n = 6,
            bs = Math.min(S * 0.16, w / (n + 3)),
            gap = bs * 0.55;
          const total = n * bs + (n - 1) * gap;
          const x0 = w / 2 - total / 2,
            yC = h / 2 - bs / 2;
          const cycle = (t * 0.55) % (n + 2.5);
          ctx.strokeStyle = 'rgba(255,255,255,0.85)';
          ctx.lineWidth = 1.6;
          ctx.lineCap = 'round';
          for (let i = 0; i < n; i++) {
            const bx = x0 + i * (bs + gap);
            const state = cycle > i + 1 ? 'done' : cycle > i ? 'active' : 'pending';
            if (i < n - 1) {
              ctx.globalAlpha = cycle > i + 1 ? 0.6 : 0.18;
              ctx.beginPath();
              ctx.moveTo(bx + bs, yC + bs / 2);
              ctx.lineTo(bx + bs + gap, yC + bs / 2);
              ctx.stroke();
              ctx.globalAlpha = 1;
            }
            ctx.save();
            if (state === 'active') {
              ctx.shadowColor = 'rgba(255,255,255,0.8)';
              ctx.shadowBlur = 14;
            }
            ctx.globalAlpha = state === 'pending' ? 0.3 : 0.95;
            ctx.strokeRect(bx, yC, bs, bs);
            ctx.restore();
            ctx.globalAlpha = 1;
            ctx.font = Math.round(bs * 0.2) + "px 'JetBrains Mono', monospace";
            ctx.textAlign = 'center';
            ctx.textBaseline = 'top';
            ctx.globalAlpha = state === 'pending' ? 0.3 : 0.75;
            ctx.fillText('STEP ' + (i + 1), bx + bs / 2, yC + bs + 14);
            ctx.globalAlpha = 1;
            ctx.textAlign = 'left';
            ctx.textBaseline = 'alphabetic';
            if (state === 'active') {
              const ph = (cycle - i) % 1;
              if (ph < 0.62) {
                const sy = yC + 4 + (ph / 0.62) * (bs - 8);
                ctx.globalAlpha = 0.7;
                ctx.beginPath();
                ctx.moveTo(bx + 4, sy);
                ctx.lineTo(bx + bs - 4, sy);
                ctx.stroke();
                ctx.globalAlpha = 0.12;
                ctx.fillRect(bx + 4, yC + 4, bs - 8, sy - yC - 4);
                ctx.globalAlpha = 1;
              }
            }
            if (state === 'done' || state === 'active') {
              const p = state === 'done' ? 1 : Math.max(0, Math.min(1, (cycle - i - 0.62) * 2.8));
              const ax = bx + bs * 0.26,
                ay = yC + bs * 0.52;
              const mx2 = bx + bs * 0.44,
                my2 = yC + bs * 0.7;
              const ex2 = bx + bs * 0.76,
                ey2 = yC + bs * 0.32;
              ctx.beginPath();
              ctx.moveTo(ax, ay);
              if (p < 0.5) {
                const q = p / 0.5;
                ctx.lineTo(ax + (mx2 - ax) * q, ay + (my2 - ay) * q);
              } else {
                ctx.lineTo(mx2, my2);
                const q = (p - 0.5) / 0.5;
                ctx.lineTo(mx2 + (ex2 - mx2) * q, my2 + (ey2 - my2) * q);
              }
              ctx.stroke();
            }
          }
          ctx.font = Math.round(Math.max(11, bs * 0.24)) + "px 'JetBrains Mono', monospace";
          ctx.textAlign = 'center';
          ctx.textBaseline = 'bottom';
          const stepIdx = Math.min(n, Math.floor(cycle) + 1);
          const dots = '.'.repeat(1 + (Math.floor(t * 3) % 3));
          ctx.globalAlpha = 0.65;
          if (cycle <= n) {
            ctx.fillText('VALIDATING STEP ' + stepIdx + ' ' + dots, w / 2, yC - bs * 0.45);
          } else {
            ctx.globalAlpha = 0.9;
            ctx.fillText('ALL STEPS PASSED', w / 2, yC - bs * 0.45);
          }
          ctx.globalAlpha = 1;
          ctx.textAlign = 'left';
          ctx.textBaseline = 'alphabetic';
          if (cycle > n) {
            const q = Math.min(1, (cycle - n) / 0.8);
            ctx.globalAlpha = (1 - q) * 0.8;
            ctx.beginPath();
            ctx.arc(w / 2, yC + bs / 2, bs * (0.9 + q * 2.2), 0, Math.PI * 2);
            ctx.stroke();
            ctx.globalAlpha = 1;
          }
        }
      };
      loop();
    }
    disconnectedCallback() {
      cancelAnimationFrame(this._raf);
      if (this._ro) this._ro.disconnect();
    }
  }
  customElements.define('particle-fx', ParticleFxElement);
}

export function ParticleFx({
  kind,
  className,
  style,
}: {
  kind: ParticleKind;
  className?: string;
  style?: React.CSSProperties;
}) {
  useEffect(() => {
    registerParticleFx();
  }, []);

  return <particle-fx kind={kind} className={className} style={style} />;
}
