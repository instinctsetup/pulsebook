const fmtPx = t => (t / 100).toLocaleString(undefined, {minimumFractionDigits: 2, maximumFractionDigits: 2});
const fmtQ = q => (q / 1e6).toFixed(4);

async function poll() {
  const r = await fetch('/api/depth');
  const d = await r.json();
  document.getElementById('c-orders').textContent = d.stats.orders_added.toLocaleString();
  document.getElementById('c-trades').textContent = d.stats.trades.toLocaleString();
  document.getElementById('c-vol').textContent = d.stats.volume_matched.toLocaleString();
  document.getElementById('c-rest').textContent = d.stats.resting.toLocaleString();
  document.getElementById('c-lat').textContent = d.last_op_ns || '-';

  const render = (el, lvls, reverse) => {
    const max = Math.max(1, ...lvls.map(l => l.qty));
    el.innerHTML = '';
    for (const l of (reverse ? [...lvls].reverse() : lvls)) {
      const w = Math.min(100, l.qty / max * 100);
      const div = document.createElement('div');
      div.className = 'lvl';
      div.innerHTML = `<div class="depthbar" style="width:${w}%"></div><span>${fmtPx(l.price)}</span><span>${fmtQ(l.qty)} BTC · ${l.orders} ord</span>`;
      el.appendChild(div);
    }
  };
  render(document.getElementById('asks'), d.asks || [], true);
  render(document.getElementById('bids'), d.bids || [], false);

  const sp = document.getElementById('spread');
  if (d.bids && d.bids.length && d.asks && d.asks.length) {
    sp.textContent = `spread ${fmtPx(d.asks[0].price - d.bids[0].price)} · mid ${fmtPx((d.asks[0].price + d.bids[0].price) / 2)}`;
  } else sp.textContent = 'book empty - seed or replay';

  const te = document.getElementById('trades');
  te.innerHTML = '';
  for (const t of (d.trades || [])) {
    const div = document.createElement('div');
    div.className = 'tr ' + (t.TakerSide === 1 ? 'buy' : 'sell');
    div.innerHTML = `<span>${t.TakerSide === 1 ? 'BUY' : 'SELL'} ${fmtQ(t.Qty)}</span><span>@ ${fmtPx(t.Price)}</span>`;
    te.appendChild(div);
  }
}

document.getElementById('btn-seed').onclick = () => fetch('/api/seed', {method:'POST'});
document.getElementById('btn-replay').onclick = () => fetch('/api/replay?n=5000', {method:'POST'});
document.getElementById('btn-send').onclick = async () => {
  const body = {
    side: document.getElementById('o-side').value,
    type: document.getElementById('o-type').value,
    price: parseInt(document.getElementById('o-price').value || '0'),
    qty: parseInt(document.getElementById('o-qty').value || '0'),
  };
  await fetch('/api/order', {method:'POST', body: JSON.stringify(body)});
  poll();
};
poll(); setInterval(poll, 400);
