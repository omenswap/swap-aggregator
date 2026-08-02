(function () {
  if (window.location.search.indexOf("error=") !== -1 && window.history.replaceState) {
    window.history.replaceState({}, "", window.location.pathname);
  }

  var form = document.getElementById("swap-form");
  if (form) {
    var fromSel = document.getElementById("from");
    var toSel = document.getElementById("to");
    var amountInput = document.getElementById("amount");
    var quotesBox = document.getElementById("quotes");
    var quotesPanel = document.getElementById("quotes-panel");
    var est = document.getElementById("est");
    var flip = document.getElementById("flip");
    var keyDialog = document.getElementById("api-key-dialog");
    var keyForm = document.getElementById("api-key-form");
    var keyInput = document.getElementById("api-key-input");
    var keyProvider = document.getElementById("api-key-provider");
    var keyError = document.getElementById("api-key-error");
    var activeKeyProvider = "";
    var timer = null;
    var pairData = [];
    var pd = document.getElementById("pair-data");
    if (pd) {
      try { pairData = JSON.parse(pd.textContent); } catch (e) {}
    }

    function updateToOptions() {
      var from = fromSel.value;
      var valid = pairData
        .filter(function (p) { return p[0] === from; })
        .map(function (p) { return p[1]; });
      if (!valid.length) return;
      var cur = toSel.value;
      toSel.innerHTML = "";
      valid.forEach(function (t) {
        var o = document.createElement("option");
        o.value = t;
        o.textContent = t;
        toSel.appendChild(o);
      });
      if (valid.indexOf(cur) !== -1) toSel.value = cur;
    }

    function fmt(s) {
      if (!s) return s;
      var dot = s.indexOf(".");
      if (dot === -1 || s.length <= 11) return s;
      var cut = s.slice(0, Math.max(dot, 11));
      if (cut.charAt(cut.length - 1) === ".") cut = cut.slice(0, -1);
      return cut;
    }

    function closeKeyDialog() {
      if (!keyDialog) return;
      keyDialog.close();
      keyInput.value = "";
      keyError.hidden = true;
      keyError.textContent = "";
      activeKeyProvider = "";
    }

    function openKeyDialog(providerName) {
      if (!keyDialog) return;
      activeKeyProvider = providerName;
      keyProvider.textContent = providerName;
      keyError.hidden = true;
      keyDialog.showModal();
      setTimeout(function () { keyInput.focus(); }, 0);
    }

    if (keyDialog) {
      document.getElementById("api-key-close").addEventListener("click", closeKeyDialog);
      document.getElementById("api-key-cancel").addEventListener("click", closeKeyDialog);
      keyDialog.addEventListener("click", function (e) {
        if (e.target === keyDialog) closeKeyDialog();
      });
      keyDialog.addEventListener("cancel", function (e) {
        e.preventDefault();
        closeKeyDialog();
      });
      keyForm.addEventListener("submit", function (e) {
        e.preventDefault();
        var key = keyInput.value.trim();
        if (!activeKeyProvider || !key) return;
        var submit = keyForm.querySelector("button[type=submit]");
        submit.disabled = true;
        submit.textContent = "saving…";
        keyError.hidden = true;
        fetch("/api/provider-keys", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ provider: activeKeyProvider, api_key: key })
        })
          .then(function (r) {
            return r.json().then(function (data) {
              if (!r.ok) throw new Error(data.error || "could not save API key");
              return data;
            });
          })
          .then(function () {
            closeKeyDialog();
            refresh();
          })
          .catch(function (err) {
            keyError.textContent = err.message;
            keyError.hidden = false;
          })
          .finally(function () {
            submit.disabled = false;
            submit.textContent = "add API key";
          });
      });
    }

    function renderQuotes(quotes) {
      quotesBox.innerHTML = "";
      var bestQuote = null;
      for (var b = 0; b < quotes.length; b++) {
        if (!quotes[b].err && !quotes[b].quote_requires_api_key &&
            !quotes[b].swap_requires_api_key && !quotes[b].link) {
          bestQuote = quotes[b];
          break;
        }
      }
      var best = bestQuote ? bestQuote.to_amount : null;
      est.textContent = best ? fmt(best) : "—";
      var checked = false;
      quotes.forEach(function (q, i) {
        var row;
        var quoteLocked = !!q.quote_requires_api_key;
        var swapLocked = !!q.swap_requires_api_key;
        var showKeyAction = quoteLocked || swapLocked || (q.has_api_key && q.err);
        if (showKeyAction) {
          row = document.createElement("div");
          row.className = "quote-row gated" + (q.err ? " err" : "");
          var mark = document.createElement("span");
          mark.className = "q-key-mark";
          mark.textContent = "key";
          row.appendChild(mark);
        } else if (q.link) {
          row = document.createElement("a");
          row.href = q.link;
          row.target = "_blank";
          row.rel = "noopener nofollow";
          row.className = "quote-row link" + (q.err ? " err" : "");
          var mark = document.createElement("span");
          mark.className = "q-ext";
          mark.textContent = "↗";
          row.appendChild(mark);
        } else {
          row = document.createElement("label");
          row.className = "quote-row" + (q.err ? " err" : bestQuote && q.provider === bestQuote.provider ? " best" : "");
          var radio = document.createElement("input");
          radio.type = "radio";
          radio.name = "provider";
          radio.value = q.provider;
          radio.setAttribute("form", "swap-form");
          radio.required = true;
          radio.disabled = !!q.err;
          if (!q.err && !checked) {
            radio.checked = true;
            checked = true;
          }
          row.appendChild(radio);
          var mark = document.createElement("span");
          mark.className = "q-mark";
          mark.textContent = "✓";
          row.appendChild(mark);
        }

        var nameWrap = document.createElement("span");
        var name = document.createElement("span");
        name.className = "q-name";
        var label = document.createElement("span");
        label.className = "q-label";
        label.textContent = q.provider;
        name.appendChild(label);
        if (bestQuote && q.provider === bestQuote.provider) {
          var tag = document.createElement("span");
          tag.className = "best-tag";
          tag.textContent = "best";
          name.appendChild(tag);
        }
        nameWrap.appendChild(name);
        if (quoteLocked || swapLocked || (q.has_api_key && q.err) ||
            (!q.err && (q.rate || q.fee || q.link))) {
          var meta = document.createElement("span");
          meta.className = "q-meta";
          meta.textContent = quoteLocked ? "API key required to fetch this quote"
            : swapLocked ? "quote available · API key required to swap here"
            : q.has_api_key && q.err ? "saved API key was rejected"
            : q.link ? "swap on " + q.provider + "'s site"
            : (q.rate ? "rate " + fmt(q.rate) : "") + (q.fee ? "  fee " + q.fee : "");
          nameWrap.appendChild(meta);
        }
        row.appendChild(nameWrap);

        if (showKeyAction) {
          var actions = document.createElement("span");
          actions.className = "q-key-actions";
          if (!q.err && q.to_amount) {
            var out = document.createElement("strong");
            out.className = "q-out";
            out.textContent = fmt(q.to_amount) + " " + toSel.value;
            actions.appendChild(out);
          }
          var keyButton = document.createElement("button");
          keyButton.type = "button";
          keyButton.className = "q-key-btn";
          keyButton.textContent = q.has_api_key ? "replace API key" : "add API key";
          keyButton.addEventListener("click", function () { openKeyDialog(q.provider); });
          actions.appendChild(keyButton);
          if (q.link) {
            var deepLink = document.createElement("a");
            deepLink.className = "q-deeplink";
            deepLink.href = q.link;
            deepLink.target = "_blank";
            deepLink.rel = "noopener nofollow";
            deepLink.textContent = "open " + q.provider + " ↗";
            actions.appendChild(deepLink);
          }
          row.appendChild(actions);
        } else if (q.err) {
          var err = document.createElement("span");
          err.className = "q-err";
          err.textContent = q.link ? "check rate on site" : q.err;
          row.appendChild(err);
        } else {
          var out = document.createElement("strong");
          out.className = "q-out";
          out.textContent = fmt(q.to_amount) + " " + toSel.value;
          row.appendChild(out);
        }
        quotesBox.appendChild(row);
      });
      quotesPanel.hidden = quotesBox.children.length === 0;
    }

    function refresh() {
      var from = fromSel.value, to = toSel.value, amount = amountInput.value.trim();
      if (!from || !to || !amount || from === to) {
        quotesPanel.hidden = true;
        est.textContent = "—";
        return;
      }
      fetch("/api/quotes?from=" + encodeURIComponent(from) +
            "&to=" + encodeURIComponent(to) +
            "&amount=" + encodeURIComponent(amount))
        .then(function (r) { return r.json(); })
        .then(function (data) { renderQuotes(data.quotes || []); })
        .catch(function () { quotesPanel.hidden = true; });
    }

    function schedule() {
      clearTimeout(timer);
      timer = setTimeout(refresh, 300);
    }

    fromSel.addEventListener("change", function () {
      updateToOptions();
      schedule();
    });
    if (flip) {
      flip.addEventListener("click", function () {
        var oldFrom = fromSel.value, oldTo = toSel.value;
        fromSel.value = oldTo;
        updateToOptions();
        for (var i = 0; i < toSel.options.length; i++) {
          if (toSel.options[i].value === oldFrom) {
            toSel.value = oldFrom;
            break;
          }
        }
        schedule();
      });
    }
    toSel.addEventListener("change", schedule);
    amountInput.addEventListener("input", schedule);
    updateToOptions();
  }

  var statusEl = document.getElementById("swap-status");
  if (statusEl) {
    var url = statusEl.dataset.poll;
    var terminal = ["completed", "expired", "refunded", "failed"];

    function applyStatus(status) {
      statusEl.textContent = status;
      statusEl.dataset.state = status;
      if (terminal.indexOf(status) !== -1) {
        statusEl.setAttribute("data-terminal", "");
      }
    }
    applyStatus(statusEl.textContent.trim());

    function poll() {
      fetch(url)
        .then(function (r) { return r.json(); })
        .then(function (s) {
          if (s.error) return;
          applyStatus(s.status);
          var fields = {
            "f-deposit-tx": s.deposit_tx_hash,
            "f-payout-tx": s.payout_tx_hash,
            "f-to-actual": s.to_amount_actual
          };
          Object.keys(fields).forEach(function (id) {
            var el = document.getElementById(id);
            if (el && fields[id]) el.textContent = fields[id];
          });
          if (terminal.indexOf(s.status) === -1) {
            setTimeout(poll, 5000);
          }
        })
        .catch(function () { setTimeout(poll, 5000); });
    }

    if (terminal.indexOf(statusEl.textContent.trim()) === -1) {
      setTimeout(poll, 5000);
    }
  }

  var copyBtn = document.getElementById("copy-addr");
  if (copyBtn) {
    copyBtn.addEventListener("click", function () {
      var addr = document.getElementById("deposit-addr").textContent.trim();
      navigator.clipboard.writeText(addr).then(function () {
        copyBtn.textContent = "copied";
        setTimeout(function () { copyBtn.textContent = "copy"; }, 1500);
      });
    });
  }
})();
