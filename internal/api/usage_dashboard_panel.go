package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const usageDashboardPanelHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Usage Dashboard</title>
<style>
:root{color-scheme:light dark;--bg:#f7f7f4;--ink:#1f2328;--muted:#667085;--line:#d8d6cf;--panel:#fff;--accent:#0f766e;--bad:#b42318}
@media (prefers-color-scheme:dark){:root{--bg:#151716;--ink:#f4f4f1;--muted:#a7adac;--line:#343937;--panel:#1e2220;--accent:#5eead4;--bad:#f97066}}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--ink);font:14px/1.45 system-ui,-apple-system,Segoe UI,sans-serif}header{display:flex;gap:16px;align-items:center;justify-content:space-between;padding:18px 22px;border-bottom:1px solid var(--line);background:var(--panel)}h1{font-size:20px;margin:0;font-weight:650}main{max-width:1280px;margin:0 auto;padding:18px 18px 34px}.toolbar{display:grid;grid-template-columns:repeat(6,minmax(120px,1fr));gap:10px;margin-bottom:16px}.field{display:flex;flex-direction:column;gap:5px}.field label{font-size:12px;color:var(--muted)}input,textarea,button{font:inherit;border:1px solid var(--line);background:var(--panel);color:var(--ink);border-radius:6px}input{height:36px;padding:7px 9px}button{height:36px;padding:0 12px;cursor:pointer}button.primary{background:var(--accent);border-color:var(--accent);color:#fff}.metrics{display:grid;grid-template-columns:repeat(5,minmax(140px,1fr));gap:10px}.metric,.section{background:var(--panel);border:1px solid var(--line);border-radius:8px}.metric{padding:13px}.metric span{display:block;color:var(--muted);font-size:12px}.metric strong{display:block;margin-top:5px;font-size:22px}.grid{display:grid;grid-template-columns:1fr 1fr;gap:14px;margin-top:14px}.section{overflow:hidden}.section h2{font-size:15px;margin:0;padding:12px 14px;border-bottom:1px solid var(--line)}table{width:100%;border-collapse:collapse}th,td{text-align:left;padding:9px 10px;border-bottom:1px solid var(--line);white-space:nowrap}th{font-size:12px;color:var(--muted);font-weight:600}tr:last-child td{border-bottom:0}.scroll{overflow:auto;max-height:460px}.prices{display:grid;grid-template-columns:1fr;gap:10px;padding:12px}.prices textarea{min-height:210px;padding:10px;font-family:ui-monospace,SFMono-Regular,Consolas,monospace;font-size:12px}.failed{color:var(--bad)}.muted{color:var(--muted)}@media(max-width:900px){.toolbar,.metrics,.grid{grid-template-columns:1fr}header{align-items:flex-start;flex-direction:column}}
</style>
</head>
<body>
<header>
  <h1>Usage Dashboard</h1>
  <div class="field" style="min-width:min(420px,100%)"><label>Management key</label><input id="key" type="password" autocomplete="current-password"></div>
</header>
<main>
  <div class="toolbar">
    <div class="field"><label>From</label><input id="from" type="date"></div>
    <div class="field"><label>To</label><input id="to" type="date"></div>
    <div class="field"><label>Provider</label><input id="provider"></div>
    <div class="field"><label>Model</label><input id="model"></div>
    <div class="field"><label>Rows</label><input id="limit" type="number" min="1" max="2000" value="200"></div>
    <div class="field"><label>&nbsp;</label><button class="primary" id="refresh">Refresh</button></div>
  </div>
  <div class="metrics">
    <div class="metric"><span>Requests</span><strong id="mRequests">0</strong></div>
    <div class="metric"><span>Total tokens</span><strong id="mTokens">0</strong></div>
    <div class="metric"><span>Input</span><strong id="mInput">0</strong></div>
    <div class="metric"><span>Output</span><strong id="mOutput">0</strong></div>
    <div class="metric"><span>Custom cost</span><strong id="mCost">0</strong></div>
  </div>
  <div class="grid">
    <section class="section"><h2>Models</h2><div class="scroll"><table><thead><tr><th>Model</th><th>Req</th><th>Tokens</th><th>Custom</th><th>Sim</th></tr></thead><tbody id="models"></tbody></table></div></section>
    <section class="section"><h2>Days</h2><div class="scroll"><table><thead><tr><th>Day</th><th>Req</th><th>Tokens</th><th>Custom</th><th>Sim</th></tr></thead><tbody id="days"></tbody></table></div></section>
  </div>
  <div class="grid">
    <section class="section"><h2>Recent</h2><div class="scroll"><table><thead><tr><th>Time</th><th>Provider</th><th>Model</th><th>In</th><th>Out</th><th>Total</th><th>Status</th></tr></thead><tbody id="records"></tbody></table></div></section>
    <section class="section"><h2>Prices</h2><div class="prices"><textarea id="prices"></textarea><button id="savePrices">Save Prices</button><div class="muted" id="state"></div></div></section>
  </div>
</main>
<script>
const $=id=>document.getElementById(id);
const fmt=n=>Number(n||0).toLocaleString();
const money=(n,c='USD')=>c+' '+Number(n||0).toFixed(4);
const key=$('key'); key.value=localStorage.getItem('cpaManagementKey')||'';
key.addEventListener('input',()=>localStorage.setItem('cpaManagementKey',key.value));
function headers(){return {'X-Management-Key':key.value,'Content-Type':'application/json'};}
async function api(path,opts={}){const r=await fetch(path,{...opts,headers:{...headers(),...(opts.headers||{})}});if(!r.ok)throw new Error(await r.text());return r.json();}
function params(){const q=new URLSearchParams();['from','to','provider','model','limit'].forEach(id=>{const v=$(id).value.trim();if(v)q.set(id,v)});return q.toString();}
function rows(id,items,fn){$(id).innerHTML=(items||[]).map(fn).join('')||'<tr><td class="muted" colspan="8">No data</td></tr>';}
function esc(v){return String(v??'').replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));}
async function refresh(){
  $('state').textContent='Loading';
  const data=await api('/v0/management/usage-dashboard?'+params());
  const cur=data.prices?.currency||'USD', total=data.total||{}, tok=total.tokens||{};
  $('mRequests').textContent=fmt(total.requests); $('mTokens').textContent=fmt(tok.total_tokens); $('mInput').textContent=fmt(tok.input_tokens); $('mOutput').textContent=fmt(tok.output_tokens); $('mCost').textContent=money(total.custom_cost,cur);
  rows('models',data.by_model,b=>'<tr><td>'+esc(b.key)+'</td><td>'+fmt(b.requests)+'</td><td>'+fmt(b.tokens?.total_tokens)+'</td><td>'+money(b.custom_cost,cur)+'</td><td>'+money(b.simulated_cost,cur)+'</td></tr>');
  rows('days',data.by_day,b=>'<tr><td>'+esc(b.key)+'</td><td>'+fmt(b.requests)+'</td><td>'+fmt(b.tokens?.total_tokens)+'</td><td>'+money(b.custom_cost,cur)+'</td><td>'+money(b.simulated_cost,cur)+'</td></tr>');
  rows('records',data.records,r=>'<tr><td>'+esc(new Date(r.timestamp).toLocaleString())+'</td><td>'+esc(r.provider)+'</td><td>'+esc(r.model)+'</td><td>'+fmt(r.tokens?.input_tokens)+'</td><td>'+fmt(r.tokens?.output_tokens)+'</td><td>'+fmt(r.tokens?.total_tokens)+'</td><td class="'+(r.failed?'failed':'')+'">'+esc(r.status_code)+'</td></tr>');
  $('prices').value=JSON.stringify(data.prices||{},null,2);
  $('state').textContent=(data.enabled?'Enabled':'Disabled')+' - '+(data.data_dir||'');
}
$('refresh').onclick=()=>refresh().catch(e=>$('state').textContent=e.message);
$('savePrices').onclick=async()=>{try{const body=$('prices').value;const saved=await api('/v0/management/usage-dashboard/prices',{method:'PUT',body});$('prices').value=JSON.stringify(saved,null,2);$('state').textContent='Saved'}catch(e){$('state').textContent=e.message}};
refresh().catch(e=>$('state').textContent=e.message);
</script>
</body>
</html>`

func (s *Server) serveUsageDashboardPanel(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.String(http.StatusOK, usageDashboardPanelHTML)
}
