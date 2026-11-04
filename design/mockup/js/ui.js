const V={};  /* view registry: V.name(arg) returns the HTML for a route */
/* Shared helpers: escaping, icons, markdown, filtering, overlays, action dispatch. */
const $=(s,r=document)=>r.querySelector(s), $$=(s,r=document)=>[...r.querySelectorAll(s)];
const esc=s=>String(s??'').replace(/[&<>"]/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
const ico=(n,cls='')=>`<i data-lucide="${n}" class="${cls}"></i>`;
const app=()=>document.getElementById('app');
const find=id=>S.items.find(i=>i.id===id);
const allTags=it=>[...it.m.map(n=>({n,m:1})),...it.a.map(n=>({n,m:0}))];
const tagNames=it=>[...it.m,...it.a];
const canon=t=>{t=t.trim().replace(/\s+/g,' ');return S.tagAlias[t]||S.tagAlias[t.toLowerCase()]||t.toLowerCase()};
function vocab(){const m={};S.items.forEach(i=>tagNames(i).forEach(t=>m[t]=(m[t]||0)+1));return m}
function md(src){
  const e=esc(src).replace(/`([^`]+)`/g,'<code>$1</code>').replace(/\*\*([^*]+)\*\*/g,'<strong>$1</strong>').replace(/(^|[^*])\*([^*]+)\*/g,'$1<em>$2</em>').replace(/\[([^\]]+)\]\(([^)]+)\)/g,'<a href="#">$1</a>');
  return e.split(/\n{2,}/).map(b=>{const L=b.split('\n');
    if(L.every(l=>/^- /.test(l)))return '<ul>'+L.map(l=>'<li>'+l.slice(2).replace(/^\[ \] /,'☐ ').replace(/^\[x\] /,'☑ ')+'</li>').join('')+'</ul>';
    if(/^#{1,3} /.test(b))return '<h4>'+b.replace(/^#+ /,'')+'</h4>';
    return '<p>'+L.join('<br>')+'</p>'}).join('');
}
function spark(pts,w=140,h=36,color='var(--acc)'){
  const mn=Math.min(...pts),mx=Math.max(...pts),r=mx-mn||1,st=w/(pts.length-1);
  const xy=pts.map((v,i)=>[i*st,h-4-(v-mn)/r*(h-10)]);
  const d=xy.map((p,i)=>(i?'L':'M')+p[0].toFixed(1)+' '+p[1].toFixed(1)).join(' ');
  const last=xy[xy.length-1];
  return `<svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" role="img" aria-label="Trend"><path d="${d} L${w} ${h} L0 ${h}Z" fill="${color}" opacity=".12"/><path d="${d}" fill="none" stroke="${color}" stroke-width="1.8" stroke-linejoin="round"/><circle cx="${last[0]}" cy="${last[1]}" r="3.2" fill="${color}"/></svg>`;
}

/* ---- filtering ---- */
function ruleTest(sp,it){
  if(sp.t==='manual'){return sp.id==='later'?!!it.rl:(sp.ids||[]).includes(it.id)}
  const r=sp.rule||{},tg=tagNames(it);
  if(r.tags_all&&!r.tags_all.every(t=>tg.includes(t)))return false;
  if(r.tags_any&&!r.tags_any.some(t=>tg.includes(t)))return false;
  if(r.kinds&&r.kinds.length&&!r.kinds.includes(it.kind))return false;
  if(r.sources&&r.sources.length&&!r.sources.includes(it.src))return false;
  if(r.within&&it.ago>r.within)return false;
  if(r.text&&![it.title,it.sum,it.note,it.site].join(' ').toLowerCase().includes(r.text.toLowerCase()))return false;
  if(r.similar_to){const o=find(r.similar_to);if(!o||o.id===it.id||!tagNames(o).some(t=>tg.includes(t)))return false}
  return true;
}
const DATE_OPTS={'7':'Past week','30':'Past month','365':'Past year'};
const isUrl=v=>/^(https?:\/\/|www\.)\S+$/i.test((v||'').trim());
function visible(ui=S.ui,ignoreQ=false){
  const q=(ignoreQ||isUrl(ui.q))?'':ui.q.trim().toLowerCase(),sp=S.spaces.find(s=>s.id===ui.space);
  return S.items.filter(i=>(ui.kind==='all'||i.kind===ui.kind)&&(!sp||ruleTest(sp,i))&&(!ui.tag||tagNames(i).includes(ui.tag))&&(!ui.source||i.src===ui.source)&&(!ui.date||i.ago<=+ui.date)
   &&(q.length<2||[i.title,i.site,tagNames(i).join(' '),i.sum,i.note,i.d?.text||'',i.d?.ocr||'',i.d?.abstract||'',KINDS[i.kind].n,(i.d?.lines||[]).map(l=>l[1]).join(' ')].join(' ').toLowerCase().includes(q)));
}
function related(it,n=6){
  const t=tagNames(it);
  return S.items.filter(o=>o.id!==it.id).map(o=>({o,s:tagNames(o).filter(x=>t.includes(x)).length*2+(o.kind===it.kind?1:0)})).filter(x=>x.s>0).sort((a,b)=>b.s-a.s).slice(0,n).map(x=>x.o);
}
function ruleText(sp){
  if(sp.t==='manual')return sp.imported?'Imported collection':'Hand-picked';
  const r=sp.rule||{},p=[];
  if(r.tags_all?.length)p.push('tagged '+r.tags_all.join(' and '));
  if(r.tags_any?.length)p.push('tagged '+r.tags_any.join(' or '));
  if(r.kinds?.length)p.push('kind '+r.kinds.join(' or '));
  if(r.sources?.length)p.push('from '+r.sources.map(s=>SOURCES[s].n).join(' or '));
  if(r.within)p.push('last '+r.within+' days');
  if(r.text)p.push('mentions “'+r.text+'”');
  if(r.similar_to)p.push('similar to “'+(find(r.similar_to)?.title||'')+'”');
  return p.join(', ')||'Everything';
}
const count=(n,w)=>n+' '+w+(n===1?'':'s');

/* ---- overlays ---- */
function modal(html,cls=''){
  closeModal();
  const a=app(),d=document.createElement('div');d.className='scrim';d.id='scrim';
  d.innerHTML=`<div class="modal ${cls}" role="dialog" aria-modal="true">${html}</div>`;
  d.style.top=a.scrollTop+'px';d.style.height='100%';d.style.left='0';d.style.right='0';
  d.addEventListener('mousedown',e=>{if(e.target===d)closeModal()});
  a.appendChild(d);a.style.overflow='hidden';lucide?.createIcons({root:d});
  const f=$('[autofocus],input,textarea',d);f&&setTimeout(()=>f.focus(),30);
  return d;
}
function closeModal(){const d=$('#scrim');if(d){d.remove();app().style.overflow=''}}
function openMenu(anchor,items,onpick){
  closeMenu();
  const a=app(),m=document.createElement('div');m.className='menu';m.id='menu';
  m.innerHTML=items.map((it,i)=>it==='-'?'<hr>':`<button data-i="${i}" ${it.on!=null?`role="menuitemradio" aria-checked="${!!it.on}"`:''}>${it.icon?ico(it.icon):''}${esc(it.label)}${it.small?`<small>${esc(it.small)}</small>`:''}</button>`).join('');
  const r=anchor.getBoundingClientRect(),ar=a.getBoundingClientRect();
  m.style.left=Math.max(8,Math.min(r.left-ar.left,ar.width-290))+'px';m.style.top=(r.bottom-ar.top+a.scrollTop+6)+'px';
  m.addEventListener('click',e=>{const b=e.target.closest('button');if(!b)return;const it=items[+b.dataset.i];closeMenu();onpick(it)});
  a.appendChild(m);lucide?.createIcons({root:m});
  setTimeout(()=>document.addEventListener('mousedown',menuOut),0);
  const f=$('button',m);f&&f.focus();
}
function menuOut(e){if(!e.target.closest('#menu'))closeMenu()}
function closeMenu(){$('#menu')?.remove();document.removeEventListener('mousedown',menuOut)}
let toastT;
function toast(msg,undo){
  let t=$('#toasts');if(!t){t=document.createElement('div');t.id='toasts';t.className='toasts';app().appendChild(t)}
  app().appendChild(t);
  t.innerHTML=`<div class="toast" role="status">${esc(msg)}${undo?'<button data-act="undo">Undo</button>':''}</div>`;
  S.undo=undo;clearTimeout(toastT);toastT=setTimeout(()=>{t.innerHTML=''},5200);
}
function confirmBox(title,body,okLabel,onok,danger=true){
  modal(`<h2 class="h2">${esc(title)}</h2><p class="muted" style="margin-top:6px">${body}</p><div class="foot"><button class="btn ghost" data-act="close">Cancel</button><button class="btn pri" data-act="confirm-ok" autofocus>${esc(okLabel)}</button></div>`);
  S.confirmOk=onok;
}

/* ---- action dispatch: any element with data-act="name" calls ACT[name](el, event) ---- */
const ACT={};
document.addEventListener('click',e=>{
  const el=e.target.closest('[data-act]');if(!el||!el.closest('#app'))return;
  const fn=ACT[el.dataset.act];if(fn){e.preventDefault();fn(el,e)}
});
document.addEventListener('keydown',e=>{
  if(e.key==='Escape'){closeMenu();closeModal();const sb=$('.savebar');sb&&(sb.hidden=true)}
});
ACT.close=()=>closeModal();
ACT['confirm-ok']=()=>{closeModal();S.confirmOk&&S.confirmOk()};
ACT.undo=()=>{S.undo&&S.undo();S.undo=null;const t=$('#toasts');t&&(t.innerHTML='')};
