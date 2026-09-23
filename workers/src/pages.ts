import { escapeHtml } from "./util";
export function page(
  title: string,
  body: string,
  nonce: string,
  script = "",
): Response {
  return new Response(
    `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>${escapeHtml(title)}</title><style nonce="${nonce}">body{font:16px system-ui;margin:3rem auto;padding:0 1rem;max-width:50rem;line-height:1.6;background:#f7f9fb;color:#17212e}main{padding:2rem;background:white;border:1px solid #dde3ec;border-radius:12px}h1{line-height:1.2}label{display:block;margin-top:1rem}input{font:inherit;padding:.7rem;width:min(100%,30rem);box-sizing:border-box}button{font:inherit;padding:.65rem 1rem;margin:.5rem .5rem .5rem 0;cursor:pointer}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#edf1f5;padding:1rem}.error{color:#9d1515}#qr svg{max-width:100%;height:auto}small{color:#536173}</style><main><h1>${escapeHtml(title)}</h1>${body}</main>${script ? `<script nonce="${nonce}">${script}</script>` : ""}</html>`,
    {
      headers: {
        "content-type": "text/html; charset=utf-8",
        "cache-control": "no-store",
        "content-security-policy": `default-src 'none'; style-src 'nonce-${nonce}'; script-src 'nonce-${nonce}'; connect-src 'self'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'`,
        "referrer-policy": "no-referrer",
        "x-content-type-options": "nosniff",
      },
    },
  );
}
export function dashboard(nonce: string): Response {
  return page(
    "GOWA · Cloudflare Workers",
    `<p>This is the native Workers port, not the upstream Go server. Pairing creates a new WhatsApp linked device.</p><p><button id="connect">Connect / Pair</button><button id="disconnect">Disconnect</button><button id="unlink">Unlink WhatsApp device</button><button id="signout">Sign out</button></p><p id="error" role="alert"></p><div id="qr"></div><pre id="status">Loading status…</pre><p>MCP endpoint: <code>/mcp</code>. Use OAuth in your MCP client.</p><small>History is best-effort. Never share QR codes, session keys or OAuth tokens. This unofficial connection can be interrupted by WhatsApp.</small>`,
    nonce,
    `
const showError=e=>document.querySelector('#error').textContent=e.message;
async function status(){const r=await fetch('/api/status');if(r.status===401){location.href='/';return;}const data=await r.json();if(!r.ok)throw Error(data.error);const svg=data.qr_svg;delete data.qr;delete data.qr_svg;document.querySelector('#status').textContent=JSON.stringify(data,null,2);document.querySelector('#qr').replaceChildren();if(svg){const doc=new DOMParser().parseFromString(svg,'image/svg+xml');document.querySelector('#qr').append(document.importNode(doc.documentElement,true));}}
async function action(path){document.querySelector('#error').textContent='';const r=await fetch(path,{method:'POST',headers:{'content-type':'application/json'},body:'{}'});if(!r.ok)throw Error((await r.json()).error||'Request failed');await status();}
document.querySelector('#connect').onclick=()=>action('/api/connect').catch(showError);
document.querySelector('#disconnect').onclick=()=>action('/api/disconnect').catch(showError);
document.querySelector('#unlink').onclick=()=>{if(confirm('Unlink this WhatsApp device and erase session keys? Stored history remains.'))action('/api/logout').catch(showError);};
document.querySelector('#signout').onclick=()=>fetch('/logout',{method:'POST'}).then(()=>location.href='/');
status().catch(showError);setInterval(()=>status().catch(showError),5000);
`,
  );
}
