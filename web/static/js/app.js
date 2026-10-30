// Guest cart stored in localStorage. The browser only keeps {sku, qty}; every price,
// discount and total shown to the customer is computed by the server.
(function () {
  'use strict';

  const KEY = 'cart.v1';

  const Cart = {
    read() {
      try {
        const items = JSON.parse(localStorage.getItem(KEY) || '[]');
        return Array.isArray(items) ? items.filter((i) => i && i.sku && i.qty > 0) : [];
      } catch (_) {
        return [];
      }
    },
    write(items, silent) {
      try { localStorage.setItem(KEY, JSON.stringify(items)); } catch (_) { /* storage full / disabled */ }
      Cart.renderBadge();
      if (!silent) document.body.dispatchEvent(new CustomEvent('cart:changed'));
    },
    add(sku, qty) {
      const items = Cart.read();
      const found = items.find((i) => i.sku === sku);
      if (found) found.qty += qty; else items.push({ sku, qty });
      Cart.write(items);
    },
    set(sku, qty) {
      let items = Cart.read();
      if (qty <= 0) items = items.filter((i) => i.sku !== sku);
      else items.forEach((i) => { if (i.sku === sku) i.qty = qty; });
      Cart.write(items);
    },
    remove(sku) { Cart.set(sku, 0); },
    clear() { Cart.write([], true); },
    count() { return Cart.read().reduce((n, i) => n + i.qty, 0); },
    serialize() { return JSON.stringify(Cart.read()); },
    renderBadge() {
      const n = Cart.count();
      document.querySelectorAll('[data-cart-count]').forEach((el) => {
        el.textContent = n > 99 ? '99+' : String(n);
        el.classList.toggle('hidden', n === 0);
      });
    },
  };
  window.Cart = Cart;

  function toast(message) {
    const el = document.getElementById('toast');
    if (!el) return;
    el.querySelector('[data-toast-text]').textContent = message;
    el.classList.remove('hidden');
    clearTimeout(toast.t);
    toast.t = setTimeout(() => el.classList.add('hidden'), 3000);
  }

  // Attach the cart to HTMX requests from elements marked data-include-cart.
  document.addEventListener('htmx:configRequest', (e) => {
    if (e.detail.elt.closest('[data-include-cart]')) e.detail.parameters.cart = Cart.serialize();
  });

  // The server corrects quantities/unavailable items; store its version without re-requesting.
  document.addEventListener('cart:normalize', (e) => {
    if (e.detail && Array.isArray(e.detail.items)) Cart.write(e.detail.items, true);
  });

  document.addEventListener('click', (e) => {
    const set = e.target.closest('[data-cart-set]');
    if (set) {
      e.preventDefault();
      Cart.set(set.dataset.cartSet, parseInt(set.dataset.qty, 10));
      return;
    }
    const rm = e.target.closest('[data-cart-remove]');
    if (rm) {
      e.preventDefault();
      Cart.remove(rm.dataset.cartRemove);
      return;
    }
    const thumb = e.target.closest('[data-gallery-thumb]');
    if (thumb) {
      const main = document.getElementById('gallery-main');
      if (main) main.src = thumb.dataset.galleryThumb;
      document.querySelectorAll('[data-gallery-thumb]').forEach((t) => t.classList.toggle('ring-2', t === thumb));
      return;
    }
    const qtyBtn = e.target.closest('[data-qty-step]');
    if (qtyBtn) {
      const input = document.getElementById(qtyBtn.dataset.target);
      const max = parseInt(input.max || '99', 10);
      const v = Math.min(Math.max(1, (parseInt(input.value, 10) || 1) + parseInt(qtyBtn.dataset.qtyStep, 10)), max);
      input.value = v;
      return;
    }
    const toggle = e.target.closest('[data-toggle]');
    if (toggle) {
      document.getElementById(toggle.dataset.toggle)?.classList.toggle('hidden');
      return;
    }
    if (e.target.closest('[data-print]')) {
      window.print();
      return;
    }
    const confirmBtn = e.target.closest('[data-confirm]');
    if (confirmBtn && !window.confirm(confirmBtn.dataset.confirm)) {
      e.preventDefault();
    }
  });

  // Product page: variant selection updates the stock hint and quantity limit.
  document.addEventListener('change', (e) => {
    const radio = e.target.closest('input[name="variant"]');
    if (!radio) return;
    const available = parseInt(radio.dataset.available, 10);
    const qty = document.getElementById('qty');
    const hint = document.getElementById('stock-hint');
    const btn = document.getElementById('add-to-cart');
    if (qty) { qty.max = Math.max(available, 1); qty.value = Math.min(parseInt(qty.value, 10) || 1, Math.max(available, 1)); }
    if (hint) hint.textContent = available > 0 ? (available <= 5 ? `Only ${available} left` : 'In stock') : 'Out of stock';
    if (btn) btn.disabled = available <= 0;
  });

  document.addEventListener('submit', (e) => {
    const form = e.target;

    if (form.matches('[data-add-to-cart]')) {
      e.preventDefault();
      const selected = form.querySelector('input[name="variant"]:checked') || form.querySelector('input[name="variant"]');
      if (!selected) return;
      const available = parseInt(selected.dataset.available, 10);
      const qty = Math.max(1, parseInt(form.querySelector('[name="qty"]').value, 10) || 1);
      const inCart = (Cart.read().find((i) => i.sku === selected.value) || { qty: 0 }).qty;
      if (inCart + qty > available) {
        toast(available - inCart > 0 ? `Only ${available - inCart} more can be added` : 'You already have all available stock in your cart');
        return;
      }
      Cart.add(selected.value, qty);
      toast('Added to cart');
      return;
    }

    if (form.matches('[data-checkout]')) {
      form.querySelector('input[name="cart"]').value = Cart.serialize();
      const btn = form.querySelector('button[type="submit"]');
      if (btn) { btn.disabled = true; btn.textContent = 'Placing order…'; }
    }
  });

  // Re-enable the checkout button if the page is restored from bfcache.
  window.addEventListener('pageshow', () => {
    const btn = document.querySelector('form[data-checkout] button[type="submit"]');
    if (btn) { btn.disabled = false; btn.textContent = btn.dataset.label || btn.textContent; }
  });

  document.addEventListener('DOMContentLoaded', () => {
    if (document.querySelector('[data-clear-cart]')) Cart.clear();
    Cart.renderBadge();
  });
})();
