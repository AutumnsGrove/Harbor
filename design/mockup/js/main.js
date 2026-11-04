/* Router, resize handling, search, keyboard shortcuts. Hash routes are plain #names. */
function parseHash(){
  const h=location.hash.slice(1)||'home';
  for(const p of ['space-edit-','item-','reader-','archive-']) if(h.startsWith(p))return {name:p.slice(0,-1),arg:h.slice(p.length)};
  if(h==='space-new')return {name:'space-edit',arg:'new'};
  if(h==='first-run')return {name:'firstrun'};
  return {name:V[h]?h:'home'};
}
function route(keep){
  const prev=S.route,r=parseHash();S.route=r;
  const a=app();if(!a)return;
  closeMenu();if(!keep)closeModal();
  if(r.name!=='space-edit'&&!keep)ED=null;
  if(r.name==='space-edit'&&!keep&&(!prev||prev.name!=='space-edit'||prev.arg!==r.arg))ED=null;
  if(r.name!=='home'&&!keep&&S.ui.selmode){S.ui.selmode=false;S.ui.sel.clear()}
  const fid=document.activeElement?.id,pos=document.activeElement?.selectionStart,scroll=keep?a.scrollTop:0,tt=$('#toasts');
  a.className='app'+(S.ui.phone?' phone':'')+(S.ui.selmode?' selmode':'');
  let view;try{view=(V[r.name]||V.home)(r.arg)}catch(err){console.error(err);view=`<div class="page"><div class="empty"><h3>This screen failed to render.</h3><p>${esc(err.message)}</p></div></div>`}
  a.innerHTML=chrome(r)+`<main id="view">${view}</main>`;
  if(tt)a.appendChild(tt);
  lucide?.createIcons({root:a});a.scrollTop=scroll;
  if(fid&&(fid==='q'||fid==='pq')){const e=document.getElementById(fid);if(e){e.focus();try{e.setSelectionRange(pos,pos)}catch(_){}}}
  document.dispatchEvent(new CustomEvent('harbor:route',{detail:r}));
}
window.addEventListener('hashchange',()=>route());
/* phone layout when the app itself is narrower than 720px (works for the device frame and real narrow windows) */
function watchSize(){
  const a=app(),ro=new ResizeObserver(()=>{const ph=a.clientWidth<720;if(ph!==S.ui.phone){S.ui.phone=ph;if(S.route)route(true)}});
  S.ui.phone=a.clientWidth<720;ro.observe(a);
}
/* search box (desktop #q, phone dock #pq) */
document.addEventListener('input',e=>{
  const t=e.target;if(t.id!=='q'&&t.id!=='pq')return;
  S.ui.q=t.value;S.ui.sel.clear();
  const sb=$('.savebar');
  if(sb){const u=isUrl(t.value);sb.hidden=!u;if(u){sb.innerHTML=`<button data-act="sv-url">${ico('link')}<span>Save <em>${esc(t.value.trim().replace(/^https?:\/\//,'').slice(0,54))}</em></span><small>↵ Enter</small></button>`;lucide?.createIcons({root:sb})}}
  if(S.route.name!=='home'){location.hash='#home';return}
  refreshHome();
});
document.addEventListener('keydown',e=>{
  if(e.target.id==='q'&&e.key==='Enter'&&isUrl(e.target.value)){e.preventDefault();ACT['sv-url']();return}
  const typing=/INPUT|TEXTAREA|SELECT/.test(e.target.tagName)||e.target.isContentEditable;
  if(typing||e.metaKey||e.ctrlKey||e.altKey||$('#scrim'))return;
  const k=e.key,now=Date.now();
  if(k==='/'){e.preventDefault();(S.ui.phone?$('#pq'):$('#q'))?.focus()}
  else if(k==='n'){e.preventDefault();ACT.compose()}
  else if(k==='?'){location.hash='#shortcuts'}
  else if(k==='g'){S.g=now}
  else if(k==='h'&&S.g&&now-S.g<900){location.hash='#home'}
  else if(k==='j'||k==='k'){const cs=$$('#home-body .bc[data-id],#home-body .pc[data-id]');if(!cs.length)return;const i=cs.indexOf(document.activeElement.closest?.('.bc,.pc'));const n=k==='j'?Math.min(cs.length-1,i+1):Math.max(0,i<0?0:i-1);cs[n].focus();cs[n].scrollIntoView({block:'nearest'})}
  else if(k==='x'){const c=document.activeElement.closest?.('.bc[data-id],.pc[data-id]');if(c){if(!S.ui.selmode){S.ui.selmode=true;app().classList.add('selmode')}S.ui.sel.has(c.dataset.id)?S.ui.sel.delete(c.dataset.id):S.ui.sel.add(c.dataset.id);refreshHome();$(`#home-body [data-id="${c.dataset.id}"]`)?.focus()}}
});
function boot(){lucide?.createIcons();watchSize();route()}
window.addEventListener('DOMContentLoaded',boot);
