let wakeLock = null;

const defaultNetworkLatencyCompensation = 0.2011;

(function () {
    if (!window.networkLatencyCompensation) {
        window.networkLatencyCompensation = defaultNetworkLatencyCompensation;
    }

    function parseHashParams() {
        var raw = location.hash.replace(/^#/, '');
        if (!raw) return {};
        if (raw[0] === '?' || raw[0] === '!') raw = raw.slice(1);
        return raw.split('&').reduce(function (acc, pair) {
            if (!pair) return acc;
            var idx = pair.indexOf('=');
            var key = idx >= 0 ? decodeURIComponent(pair.slice(0, idx)) : decodeURIComponent(pair);
            var val = idx >= 0 ? decodeURIComponent(pair.slice(idx + 1)) : '';
            acc[key] = val;
            return acc;
        }, {});
    }

    function buildHash(params) {
        var parts = [];
        for (var k in params) {
            if (!Object.prototype.hasOwnProperty.call(params, k)) continue;
            var v = params[k];
            if (v === undefined || v === null) continue;
            parts.push(encodeURIComponent(k) + '=' + encodeURIComponent(String(v)));
        }
        return parts.join('&');
    }

    function applyLatencyFromHash() {
        var params = parseHashParams();
        if (params.latency !== undefined && params.latency !== '') {
            var parsed = parseFloat(params.latency);
            if (!isNaN(parsed)) {
                console.info(`Changed networkLatencyCompensation! ${window.networkLatencyCompensation} -> ${parsed}`);
                window.networkLatencyCompensation = parsed;
            }
        }
    }

    function ensureHashLatency() {
        var raw = location.hash.replace(/^#/, '');
        if (!raw) {
            var params = { latency: String(window.networkLatencyCompensation) };
            var newHash = buildHash(params);
            history.replaceState(null, '', location.pathname + location.search + (newHash ? '#' + newHash : ''));
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function () {
            if (!location.hash || /^#\s*$/.test(location.hash)) ensureHashLatency();
            else applyLatencyFromHash();
        });
    } else {
        if (!location.hash || /^#\s*$/.test(location.hash)) ensureHashLatency();
        else applyLatencyFromHash();
    }

    window.addEventListener('hashchange', applyLatencyFromHash);

    window.setLatencyHash = function (value) {
        var params = parseHashParams();
        params.latency = String(value);
        var newHash = buildHash(params);
        history.replaceState(null, '', location.pathname + location.search + (newHash ? '#' + newHash : ''));
    };
})();

async function startScreenLock() {
    if (!("wakeLock" in navigator)) {
        console.warn("Wake lock is not supported by this browser.")
        return
    }

    try {
        wakeLock = await navigator.wakeLock.request('screen');
        console.info("Экран успешно заблокирован от засыпания :)");
    } catch (err) {
        console.error('Ошибка запроса блокировки экрана', err);
    }
}

(() => startScreenLock())();

function stopScreenLock() {
    if (!wakeLock) return;
    wakeLock.release();
}

function upperBound(arr, x) {
    let l = 0, r = arr.length;
    while (l < r) {
        const m = (l + r) >> 1;
        if (arr[m] <= x) l = m + 1;
        else r = m;
    }
    return l;
}

function initxmlhttp() {
    var xmlhttp = false;
    /*@cc_on @*/
    /*@if (@_jscript_version >= 5)
    // JScript gives us Conditional compilation, we can cope with old IE versions.
    // and security blocked creation of the objects.
     try { xmlhttp = new ActiveXObject("Msxml2.XMLHTTP"); } catch (e) {
      try { xmlhttp = new ActiveXObject("Microsoft.XMLHTTP"); } catch (E) {
       xmlhttp = false;
      }
     }
    @end @*/
    if (!xmlhttp && typeof XMLHttpRequest != 'undefined') {
        try {
            xmlhttp = new XMLHttpRequest();
        } catch (e) {
            xmlhttp = false;
        }
    }
    if (!xmlhttp && window.createRequest) {
        try {
            xmlhttp = window.createRequest();
        } catch (e) {
            xmlhttp = false;
        }
    }
    return xmlhttp;
}

const wsURL = (window.location.protocol === "https:" ? "wss://" : "ws://") + window.location.host + "/ws";
var ws = null;

var g_wwr_timer_freq = 100;
var g_wwr_req_list = "", g_wwr_req_list2 = "";
var g_wwr_req_recur = [], g_wwr_req = null, g_wwr_timer = null, g_wwr_timer2 = null;
var g_wwr_errcnt = 0;

function wwr_run_update() {
    g_wwr_timer = null;
    if (!g_wwr_req) g_wwr_req = initxmlhttp();
    if (!g_wwr_req) {
        alert("no xml http support");
        return;
    }

    // populate any periodic requests
    var d = (new Date).getTime();
    for (var x = 0; x < g_wwr_req_recur.length; x++) {
        if (g_wwr_req_recur[x][2] < d) {
            g_wwr_req_recur[x][2] = d + g_wwr_req_recur[x][1];
            g_wwr_req_list2 += g_wwr_req_recur[x][0] + ";";
        }
    }
    g_wwr_req_list += g_wwr_req_list2;
    g_wwr_req_list2 = "";

    if (g_wwr_req_list != "") {
        g_wwr_req.open("GET", "/_/" + g_wwr_req_list, true);
        g_wwr_req.onreadystatechange = function () {
            if (g_wwr_req.readyState == 4) {
                if (g_wwr_timer2) {
                    clearTimeout(g_wwr_timer2);
                    g_wwr_timer2 = null;
                }
                if (g_wwr_req.responseText != "") {
                    g_wwr_errcnt = 0;
                    wwr_onreply(g_wwr_req.responseText, d);
                } else if (g_wwr_req.getResponseHeader("Server") == null) {
                    if (g_wwr_errcnt < 8) g_wwr_errcnt++;
                }
                if (g_wwr_errcnt > 2) g_wwr_timer = window.setTimeout(wwr_run_update, 100 << (g_wwr_errcnt - 3));
                else wwr_run_update();
            }
        };

        if (g_wwr_timer2) clearTimeout(g_wwr_timer2);
        g_wwr_timer2 = window.setTimeout(function () {
            g_wwr_timer2 = null;
            if (g_wwr_req.readyState != 0 && g_wwr_req.readyState != 4) {
                if (g_wwr_timer) {
                    clearTimeout(g_wwr_timer);
                    g_wwr_timer = null;
                }
                g_wwr_req.abort();
                if (g_wwr_errcnt < 8) g_wwr_errcnt++;

                if (g_wwr_errcnt > 2) g_wwr_timer = window.setTimeout(wwr_run_update, 100 << (g_wwr_errcnt - 3));
                else wwr_run_update();
            }
        }, 3000);

        g_wwr_req_list = "";
        g_wwr_req.send(null);
    } else {
        g_wwr_timer = window.setTimeout(wwr_run_update, g_wwr_timer_freq);
    }
}

const wsMode = 'WebSocket' in window || 'MozWebSocket' in window;

function wwr_start() {
    wwr_run_update();
}

function wwr_req(name) {
    g_wwr_req_list += name + ";";
}

function wwr_req_recur(name, interval) {
    if (wsMode) wwr_req_recur_websocket(name); else g_wwr_req_recur.push([name, interval, 0]);
}

function wwr_req_recur_cancel(name) {
    for (let i = 0; i < g_wwr_req_recur.length; ++i) {
        if (g_wwr_req_recur[i] && g_wwr_req_recur[i][0] === name) {
            g_wwr_req_recur.splice(i, 1);
            break;
        }
    }
}

function mkvolstr(vol) {
    var v = parseFloat(vol);
    if (v < 0.00000002980232) return "-inf dB";
    v = Math.log(v) * 8.68588963806;
    return v.toFixed(2) + " dB";
}

function mkpanstr(pan) {
    if (Math.abs(pan) < 0.001) return "center";
    if (pan > 0) return (pan * 100).toFixed(0) + "%R";
    return (pan * -100).toFixed(0) + "%L";
}

function simple_unescape(v) {
    return String(v).replace(/\\t/g, "\t").replace(/\\n/g, "\n").replace(/\\\\/g, "\\");
}

function wwr_req_recur_websocket(name) {
    ws = createWebSocket(wsURL, function (data) {
        if (typeof wwr_onreply === 'function')
            wwr_onreply(data);
    })
}

function createWebSocket(url, onMessageCallback) {
    let ws;
    let reconnectDelay = 1000;     // начальный интервал переподключения (1 секунда)
    let shouldReconnect = true;

    function connect() {
        ws = new WebSocket(url);

        ws.onopen = function (ev) {
            console.log("WS connected");
            reconnectDelay = 1000;
        };

        ws.onmessage = function (msg) {
            onMessageCallback(msg.data);
        };

        ws.onerror = function (err) {
            console.error("WS error:", err);
            ws.close();
        };

        ws.onclose = function (ev) {
            console.warn("WS closed, will try reconnect. Code:", ev.code, "Reason:", ev.reason);
            if (!shouldReconnect) {
                return;
            }
            setTimeout(function () {
                // reconnectDelay = Math.min(maxDelay, reconnectDelay * 1.5);
                connect();
            }, reconnectDelay);
        };
    }

    connect();

    return {
        send: function (data) {
            if (ws && ws.readyState === WebSocket.OPEN) {
                ws.send(data);
            } else {
                console.warn("WS not open, cannot send:", data);
            }
        },
        isReady: function () {
            return ws.readyState === WebSocket.OPEN;
        },
        close: function () {
            shouldReconnect = false;
            if (ws) {
                ws.close();
            }
        }
    };
}