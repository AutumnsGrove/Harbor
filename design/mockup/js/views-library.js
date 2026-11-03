/* Library views: chrome, Home, cards, composer, bulk actions, needs attention, first run. */
const MARK=`<svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="8" r="3.3" fill="var(--amb)"/><path d="M3 16.5h18" stroke="var(--acc)" stroke-width="1.8"/><path d="M6 20.5h12" stroke="var(--dim)" stroke-width="1.6"/></svg>`;
const attnCount=()=>S.failed.length;

function chrome(route){
  const a=attnCount();
  const bell=`<button class="ibtn" data-act="go" data-to="attention" aria-label="Needs attention: ${a}" title="Needs attention">${ico('triangle-alert')}${a?'<i style="position:absolute;right:6px;top:6px;width:9px;height:9px;border-radius:50%;background:var(--amb);box-shadow:0 0 0 2px var(--bg)"></i>':''}</button>`;
  if(S.ui.phone){
    if(route.name!=='home')return '';
    return `<header class="top"><button class="brand" data-act="go" data-to="home">${MARK}Harbor</button><span class="sp"></span><span style="position:relative">${bell}</span>${S.ui.selmode?'<button class="btn sm pri" data-act="select">Done</button>':`<button class="ibtn" data-act="select" aria-label="Select items">${ico('circle-check')}</button>`}<button class="ibtn" data-act="go" data-to="prefs" aria-label="Settings">${ico('settings')}</button></header>`;
  }
  if(route.name==='reader'||route.name==='archive')return '';
  return `<header class="top"><button class="brand" data-act="go" data-to="home">${MARK}Harbor</button>
  <div class="searchbox"><label>${ico('search')}<input id="q" type="text" placeholder="Search everything, or paste a link to save" aria-label="Search or save" autocomplete="off" value="${esc(S.ui.q)}"><kbd>/</kbd></label><div class="savebar" hidden></div></div>
  <span class="sp"></span><span style="position:relative">${bell}</span><button class="btn pri savebtn" data-act="compose">${ico('plus')}Save</button><button class="ibtn" data-act="go" data-to="prefs" aria-label="Settings" title="Settings">${ico('settings')}</button></header>`;
}
/* page header: breadcrumb on desktop, sticky bar on phone */
function head(title,{back='home',label='Library',right=''}={}){
  return `<nav class="crumb" aria-label="Breadcrumb"><button data-act="go" data-to="${back}">${ico('chevron-left')}${esc(label)}</button><span class="sp"></span>${right}</nav>
  <div class="phead"><button class="ibtn" data-act="go" data-to="${back}" aria-label="Back to ${esc(label)}">${ico('chevron-left')}</button><div class="t">${esc(title)}</div><div class="row" style="gap:6px;min-width:40px;justify-content:flex-end">${right}</div></div>`;
}

/* ---- cards ---- */
function cover_(it){return it.cv?cover(it):''}
function pendingCard(p){
  if(S.ui.phone)return `<article class="pc pending"><div class="pc-m ph"></div><div class="pc-b"><h3>${esc(p.title)}</h3><p class="mt">${esc(p.lbl)} · ${esc(p.age)}</p></div></article>`;
  return `<article class="bc pending"><div class="bc-m ph"><span>${esc(p.lbl)}</span></div><div class="bc-t"><h3>${esc(p.title)}</h3><p class="sub"><span>${esc(p.site)}</span><span>· ${esc(p.age)}</span></p></div></article>`;
}
function card(it){
  const K=KINDS[it.kind],on=S.ui.sel.has(it.id),sel=`<span class="sel">${ico('check')}</span>`;
  if(it.kind==='note'||it.kind==='tweet'){
    const tx=it.kind==='tweet'?it.d.text:esc(it.note.split('\n')[0]+(it.note.includes('\n')?'…':'')).replace(/`([^`]+)`/g,'<code class="code">$1</code>').replace(/\*\*([^*]+)\*\*/g,'$1');
    return `<article class="bc${on?' on':''}" data-id="${it.id}" tabindex="0" role="link" aria-label="${esc(it.title)}">${sel}<div class="bc-text${it.kind==='tweet'?' q':''}"><p class="tx">${tx}</p><p class="sub dim row" style="gap:6px">${ico(K.i)}<span>${it.kind==='tweet'?esc(it.title):'Note'} · ${it.ago}d</span></p></div></article>`;
  }
  const badge=it.d.dur?`<div class="play"><span>${ico('play')}</span></div><span class="badge" style="right:10px;bottom:10px">${it.d.dur}</span>`:it.dead?`<span class="badge warn">${ico('unlink')}Link dead · archive kept</span>`:'';
  const more=(it.sum?esc(it.sum).slice(0,110)+(it.sum.length>110?'…':''):'')+(it.sum?'<br>':'')+`<b>${tagNames(it).slice(0,4).join(' · ')}</b>`;
  return `<article class="bc${it.dead?' dead':''}${on?' on':''}" data-id="${it.id}" tabindex="0" role="link" aria-label="${esc(it.title)}"><div class="bc-m">${cover_(it)}${sel}<div class="kchip">${ico(K.i)}${K.n}</div>${it.rl?`<div class="rl" title="Read later">${ico('clock')}</div>`:''}${badge}</div><div class="bc-t"><h3>${esc(it.title)}</h3><p class="sub"><span>${esc(it.site)}</span><span>· ${it.ago}d</span></p><p class="more">${more}</p></div></article>`;
}
function pcard(it){
  const K=KINDS[it.kind],on=S.ui.sel.has(it.id);
  const badge=it.d.dur?`<span class="badge">${it.d.dur}</span>`:it.dead?`<span class="badge warn">Link dead</span>`:'';
  const txt=it.kind==='tweet'?it.d.text:it.kind==='note'?it.note.split('\n')[0].replace(/[`*]/g,''):(it.sum||'');
  const body=(it.kind==='note'||it.kind==='tweet')?`<p class="ns big">${esc(txt)}</p>`:`<h3>${esc(it.title)}</h3>${txt?`<p class="ns">${esc(txt)}</p>`:''}`;
  return `<article class="pc${it.dead?' dead':''}${on?' on':''}" data-id="${it.id}" tabindex="0" role="link" aria-label="${esc(it.title)}"><span class="ck">${ico('check')}</span>${it.cv?`<div class="pc-m">${cover(it)}${badge}</div>`:''}<div class="pc-b">${body}<div class="pills">${it.rl?`<span class="pill amb">${ico('clock')}Read later</span>`:''}<span class="pill">${ico(K.i)}${K.n}</span>${tagNames(it).slice(0,2).map(t=>`<span class="pill">${esc(t)}</span>`).join('')}</div><p class="mt">${esc(it.site)} · ${it.ago}d</p></div></article>`;
}
const anyCard=it=>S.ui.phone?pcard(it):card(it);

/* ---- home ---- */
function homeBody(){
  const u=S.ui,list=visible(),sp=S.spaces.find(s=>s.id===u.space);
  const idle=(!u.q.trim()||isUrl(u.q))&&u.kind==='all'&&!u.space&&!u.tag&&!u.source&&!u.date;
  const act=[];
  if(u.tag)act.push(['tag',u.tag]);if(u.source)act.push(['source',SOURCES[u.source].n]);if(u.date)act.push(['date',DATE_OPTS[u.date]]);
  const sugg=idle&&S.suggestion?`<div class="sugg">${ico('sparkles')}<div class="grow" style="flex:1"><b>${esc(S.suggestion.name)}?</b> <span class="muted">${esc(S.suggestion.why)}</span></div><button class="btn sm" data-act="sugg-open">Review</button><button class="btn sm ghost" data-act="sugg-no">Not now</button></div>`:'';
  const attn=idle&&attnCount()&&!S.dismissAttn?`<div class="callout" style="margin-bottom:22px"><span>${count(attnCount(),'save')} couldn't be read: ${esc(S.failed[0].url)}</span><span class="sp"></span><button class="lnk" data-act="go" data-to="attention">Review</button><button class="lnk q" data-act="dismiss-attn">Dismiss</button></div>`:'';
  const pend=idle?S.pending.map(pendingCard).join(''):'';
  const cards=list.map(anyCard).join('');
  const emptyMsg=u.q.trim()?`Nothing matches “${esc(u.q.trim())}”.`:'Nothing in this view yet.';
  const empty=list.length||pend?'':`<div class="empty" style="column-span:all"><h3>${emptyMsg}</h3><p>Search covers titles, notes, page text, OCR and transcripts. ${u.q.trim()?'Try fewer words, or save it as a note.':'Saved items will appear here.'}</p></div>`;
  return `${S.ui.phone?`<h1 class="ptitle">${esc(sp?sp.n:'All items')}</h1>`:''}
  <div class="chips" role="group" aria-label="Spaces"><button class="chip" data-act="space" data-id="" aria-pressed="${!u.space}">Everything</button>${S.spaces.map(s=>`<button class="chip" data-act="space" data-id="${s.id}" aria-pressed="${u.space===s.id}">${s.id==='later'?ico('clock'):''}${esc(s.n)}<small>${s.lib}</small></button>`).join('')}<button class="chip" data-act="${S.ui.phone?'sheet':'go'}" data-to="spaces" style="color:var(--dim)">${ico('layout-grid')}All spaces</button></div>
  <div class="kinds" role="group" aria-label="Kinds"><button class="chip sm" data-act="kind" data-k="all" aria-pressed="${u.kind==='all'}">All kinds</button>${Object.keys(KINDS).map(k=>`<button class="chip sm" data-act="kind" data-k="${k}" aria-pressed="${u.kind===k}">${KINDS[k].n}</button>`).join('')}</div>
  <div class="filters"><button class="chip ghost" data-act="f-tag" aria-pressed="${!!u.tag}">${ico('tag')}Tag</button><button class="chip ghost" data-act="f-source" aria-pressed="${!!u.source}">${ico('send')}Source</button><button class="chip ghost" data-act="f-date" aria-pressed="${!!u.date}">${ico('calendar')}Date</button>
   ${act.map(([k,v])=>`<span class="token">${esc(v)}<button data-act="f-clear" data-k="${k}" aria-label="Remove ${esc(v)} filter">${ico('x')}</button></span>`).join('')}
   <span class="sp"></span>${S.ui.phone?'':`<span class="muted num" style="font-size:var(--t-s)">${idle?`${list.length+S.pending.length} of ${S.items.length>20?'1,284':'1,284'} shown`:count(list.length,'result')}</span><button class="btn sm ${u.selmode?'pri':''}" data-act="select">${ico('circle-check')}${u.selmode?'Done':'Select'}</button>`}</div>
  ${sp&&sp.t!=='manual'?`<p class="muted" style="margin:-8px 0 20px;font-size:var(--t-s)">${esc(sp.n)} · ${sp.t==='auto'?'Auto Space':'Saved search'} · ${esc(ruleText(sp))} <button class="chip sm" data-act="go" data-to="space-edit-${sp.id}" style="padding:2px 10px;margin-left:6px">Edit rule</button></p>`:''}
  ${attn}${sugg}<div class="feed${u.selmode?' selmode':''}">${pend}${cards}${empty}</div>`;
}
function bulkHTML(){
  const n=S.ui.sel.size;if(!S.ui.selmode||S.ui.phone)return '';
  return `<div class="bulk" role="toolbar" aria-label="Bulk actions"><span class="ct num">${n} selected</span><button class="btn sm ghost" data-act="b-del" ${n?'':'disabled'}>${ico('trash-2')}Delete</button><button class="btn sm ghost" data-act="b-tag" ${n?'':'disabled'}>${ico('tag')}Tag</button><button class="btn sm ghost" data-act="b-space" ${n?'':'disabled'}>${ico('layers')}Add to Space</button><button class="btn sm ghost" data-act="b-refresh" ${n?'':'disabled'}>${ico('refresh-cw')}Refresh</button></div>`;
}
function dockHTML(){
  const u=S.ui;
  if(u.selmode)return `<div class="dock"><div><div class="ps bk"><span class="ct num">${u.sel.size} selected</span><button data-act="b-del">${ico('trash-2')}Delete</button><button data-act="b-tag">${ico('tag')}Tag</button><button data-act="b-space">${ico('layers')}Space</button><button data-act="b-refresh">${ico('refresh-cw')}Refresh</button></div></div></div>`;
  return `<div class="dock"><div><button class="rb ${u.space==='later'?'on':''}" data-act="space" data-id="${u.space==='later'?'':'later'}" aria-label="Read later">${ico('clock')}</button><label class="ps">${ico('search')}<input id="pq" type="search" placeholder="Search" aria-label="Search" value="${esc(u.q)}"></label><button class="rb acc" data-act="compose" aria-label="Save">${ico('plus')}</button></div></div>`;
}
function phoneHome(){return ''}
V.home=()=>{
  const u=S.ui;
  const top='';
  return `${top}<div class="page" id="home-body">${homeBody()}</div><div id="bulk">${bulkHTML()}</div>${S.ui.phone?dockHTML():''}`;
};
function refreshHome(){
  const b=$('#home-body');if(!b)return;
  b.innerHTML=homeBody();$('#bulk').innerHTML=bulkHTML();
  if(S.ui.phone){const d=$('.dock');if(d){const q=$('#pq');const had=document.activeElement===q,pos=q?.selectionStart;d.outerHTML=dockHTML();if(had){const nq=$('#pq');nq.focus();nq.setSelectionRange(pos,pos)}}}
  lucide?.createIcons({root:app()});
}
/* actions */
ACT.go=el=>{location.hash='#'+el.dataset.to};
ACT.space=el=>{S.ui.space=el.dataset.id||null;S.ui.sel.clear();refreshHome()};
ACT.kind=el=>{const k=el.dataset.k;S.ui.kind=(S.ui.kind===k&&k!=='all')?'all':k;refreshHome()};
ACT.select=()=>{S.ui.selmode=!S.ui.selmode;S.ui.sel.clear();app().classList.toggle('selmode',S.ui.selmode);refreshHome()};
ACT['f-clear']=el=>{S.ui[el.dataset.k]=null;refreshHome()};
ACT['f-tag']=el=>{const v=vocab(),top=Object.entries(v).sort((a,b)=>b[1]-a[1]).slice(0,12);openMenu(el,top.map(([t,n])=>({label:t,small:n,v:t,on:S.ui.tag===t})),i=>{S.ui.tag=S.ui.tag===i.v?null:i.v;refreshHome()})};
ACT['f-source']=el=>openMenu(el,Object.entries(SOURCES).map(([k,s])=>({label:s.n,icon:s.i,v:k,on:S.ui.source===k})),i=>{S.ui.source=S.ui.source===i.v?null:i.v;refreshHome()});
ACT['f-date']=el=>openMenu(el,Object.entries(DATE_OPTS).map(([k,l])=>({label:l,v:k,on:S.ui.date===k})),i=>{S.ui.date=S.ui.date===i.v?null:i.v;refreshHome()});
ACT['dismiss-attn']=()=>{S.dismissAttn=true;refreshHome()};
ACT['sugg-no']=()=>{S.suggestion=null;refreshHome();toast('Okay, I will not suggest that again.')};
ACT['sugg-open']=()=>{S.prefill=true;ED=null;location.hash='#space-new'};
ACT.sheet=el=>{
  modal(`<h2 class="h2" style="margin-bottom:10px">Spaces</h2><div class="rows">${[{id:'',n:'Everything',lib:'1,284',t:'all'},...S.spaces].map(s=>`<button class="link-row" data-act="sheet-pick" data-id="${s.id}" style="${S.ui.space===(s.id||null)?'color:var(--acc)':''}"><span class="grow">${esc(s.n)}</span><span class="s">${s.t==='all'?'':s.t+' · '}${s.lib}</span></button>`).join('')}</div><div class="foot"><button class="btn" data-act="go" data-to="spaces">Manage Spaces</button></div>`);
};
ACT['sheet-pick']=el=>{closeModal();S.ui.space=el.dataset.id||null;refreshHome()};
/* card clicks and keyboard */
document.addEventListener('click',e=>{
  const c=e.target.closest('#home-body [data-id].bc, #home-body [data-id].pc');if(!c||e.target.closest('[data-act]'))return;
  if(S.ui.selmode){const id=c.dataset.id;S.ui.sel.has(id)?S.ui.sel.delete(id):S.ui.sel.add(id);refreshHome();return}
  location.hash='#item-'+c.dataset.id;
});
document.addEventListener('keydown',e=>{const c=e.target.closest?.('.bc[data-id],.pc[data-id]');if(c&&(e.key==='Enter'||e.key===' ')){e.preventDefault();c.click()}});
/* bulk */
ACT['b-del']=()=>{const ids=[...S.ui.sel];if(!ids.length)return;confirmBox(`Delete ${count(ids.length,'item')}?`,'They move to Recently deleted in Settings and are purged after 30 days.','Delete',()=>{trash(ids);S.ui.sel.clear();refreshHome();toast(`${count(ids.length,'item')} moved to Recently deleted`,()=>restore(ids))})};
ACT['b-tag']=()=>{if(!S.ui.sel.size)return;modal(`<h2 class="h2">Add a tag</h2><p class="muted">Applies to ${count(S.ui.sel.size,'item')}. Existing tags and aliases are reused.</p><div class="field" style="margin-top:16px"><input class="in" id="bt" placeholder="e.g. homelab" list="vocab" autofocus><datalist id="vocab">${Object.keys(vocab()).map(t=>`<option value="${esc(t)}">`).join('')}</datalist></div><div class="foot"><button class="btn ghost" data-act="close">Cancel</button><button class="btn pri" data-act="b-tag-ok">Add tag</button></div>`)};
ACT['b-tag-ok']=()=>{const t=canon($('#bt').value);if(!t)return;S.ui.sel.forEach(id=>{const it=find(id);if(!tagNames(it).includes(t))it.m.push(t)});closeModal();toast(`Tagged ${count(S.ui.sel.size,'item')} “${t}”`);S.ui.sel.clear();refreshHome()};
ACT['b-space']=el=>{if(!S.ui.sel.size)return;const man=S.spaces.filter(s=>s.t==='manual');openMenu(el,man.map(s=>({label:s.n,v:s.id})),i=>{S.ui.sel.forEach(id=>{const it=find(id);if(i.v==='later')it.rl=1;else{const sp=S.spaces.find(s=>s.id===i.v);sp.ids=sp.ids||[];if(!sp.ids.includes(id))sp.ids.push(id)}});toast(`Added ${count(S.ui.sel.size,'item')} to ${man.find(s=>s.id===i.v).n}`);S.ui.sel.clear();refreshHome()})};
ACT['b-refresh']=()=>{if(!S.ui.sel.size)return;toast(`Refreshing ${count(S.ui.sel.size,'item')}: metadata, screenshot and archive`);S.ui.sel.clear();refreshHome()};
function trash(ids){ids.forEach(id=>{const i=S.items.findIndex(x=>x.id===id);if(i<0)return;const it=S.items.splice(i,1)[0];S.deleted.unshift({id:it.id,kind:it.kind,title:it.title,site:it.site,left:30,cv:it.cv,_it:it})})}
function restore(ids){ids.forEach(id=>{const i=S.deleted.findIndex(x=>x.id===id);if(i<0)return;const d=S.deleted.splice(i,1)[0];if(d._it)S.items.unshift(d._it);route()})}

/* ---- composer ---- */
const COMP={url:'',note:'',imgs:[],later:false};
const SAMPLE_IMGS=[{id:'c1',cv:{t:'tex',ar:1,c:['#3a2f2c','#a07f70','#1f1a19']}},{id:'c2',cv:{t:'grad',ar:1,c:['#1d4e63','#0b2433','#eaa84a']}},{id:'c3',cv:{t:'grad',ar:1,c:['#4a2a4f','#1c0f20','#ff5f93']}}];
function composerHTML(){
  const existing=COMP.url.trim()&&S.items.find(i=>i.url&&i.url===COMP.url.trim().replace(/^https?:\/\//,'').replace(/[?#].*$/,'').replace(/\/$/,''));
  return `<h2 class="h2">Save to Harbor</h2><p class="muted" style="margin-top:4px">A link, a thought, or images. Tags and kind are filled in for you.</p>
  <div class="stack" style="margin-top:18px">
   <div class="field"><label for="c-url">Link</label><input class="in" id="c-url" placeholder="Paste a URL" value="${esc(COMP.url)}" autocomplete="off"></div>
   ${existing?`<div class="callout"><span>Already saved as <b>${esc(existing.title)}</b>. Saving again appends your note, merges tags and refreshes the archive.</span></div>`:''}
   <div class="field"><label for="c-note">Note <span class="dim">(Markdown)</span></label><textarea class="ta" id="c-note" placeholder="Why does this matter?">${esc(COMP.note)}</textarea></div>
   <div class="field"><label>Images <span class="dim num">${COMP.imgs.length} of 10</span></label><div class="drop">${COMP.imgs.map((m,i)=>`<div class="thumb">${cover(m)}<button data-act="c-rm" data-i="${i}" aria-label="Remove image">${ico('x')}</button></div>`).join('')}${COMP.imgs.length<10?`<button class="addimg" data-act="c-add" aria-label="Add image">${ico('image-plus')}</button>`:''}${COMP.imgs.length?'':'<span>Drop, paste or add up to 10 images</span>'}</div></div>
   <div class="row"><button class="switch" role="switch" aria-checked="${COMP.later}" data-act="c-later" aria-label="Add to Read later"></button><span>Add to Read later</span></div>
  </div>
  <div class="foot"><span class="hint" style="margin-right:auto">${S.ui.phone?'':'<kbd>⌘</kbd> <kbd>↵</kbd> to save'}</span><button class="btn ghost" data-act="close">Cancel</button><button class="btn pri" data-act="c-save">${existing?'Merge into item':'Save'}</button></div>`;
}
ACT.compose=()=>{COMP.url='';COMP.note='';COMP.imgs=[];COMP.later=false;modal(composerHTML());$('#c-url').focus();bindComposer()};
function recomp(){const f=document.activeElement?.id,pos=document.activeElement?.selectionStart;$('#scrim .modal').innerHTML=composerHTML();lucide?.createIcons({root:$('#scrim')});bindComposer();if(f){const e=document.getElementById(f);if(e){e.focus();try{e.setSelectionRange(pos,pos)}catch(_){}}}}
function bindComposer(){const u=$('#c-url'),n=$('#c-note');
  u.oninput=()=>{const was=!!findExisting(COMP.url);COMP.url=u.value;if(!!findExisting(COMP.url)!==was)recomp()};
  n.oninput=()=>COMP.note=n.value;
  n.onkeydown=u.onkeydown=e=>{if(e.key==='Enter'&&(e.metaKey||e.ctrlKey))ACT['c-save']()}}
const findExisting=u=>u.trim()&&S.items.find(i=>i.url&&i.url===u.trim().replace(/^https?:\/\//,'').replace(/[?#].*$/,'').replace(/\/$/,''));
ACT['c-add']=()=>{COMP.imgs.push(SAMPLE_IMGS[COMP.imgs.length%3]);recomp()};
ACT['c-rm']=el=>{COMP.imgs.splice(+el.dataset.i,1);recomp()};
ACT['c-later']=()=>{COMP.later=!COMP.later;recomp()};
ACT['c-save']=()=>{
  const url=COMP.url.trim(),note=COMP.note.trim();
  if(!url&&!note&&!COMP.imgs.length){toast('Add a link, a note or an image first.');return}
  const ex=findExisting(url);
  if(ex){ex.note=(ex.note?ex.note+'\n\n':'')+`*${'Nov 3'}* ${note}`;ex.ago=0;if(COMP.later)ex.rl=1;closeModal();toast('Merged into “'+ex.title+'”');route();return}
  saveNew({url,note,imgs:COMP.imgs.slice(),later:COMP.later,src:'app'});closeModal();
};
function saveNew({url,note,imgs,later,src}){
  const clean=url.replace(/^https?:\/\//,'').replace(/[?#].*$/,'').replace(/\/$/,'');
  const site=clean.split('/')[0]||'In-app',id='n'+Date.now();
  const p={id,title:clean||(note.split('\n')[0].slice(0,60))||'Image',site,src,age:'now',lbl:url?'Reading the page':'Saving…'};
  S.pending.unshift(p);S.ui.q='';S.ui.kind='all';S.ui.space=null;S.ui.tag=null;S.ui.source=null;S.ui.date=null;
  if(location.hash!=='#home')location.hash='#home';else refreshHome();
  setTimeout(()=>{
    S.pending=S.pending.filter(x=>x.id!==id);
    const kind=url?'article':imgs.length?'image':'note';
    const it={id,kind,title:url?clean:(imgs.length&&!note?'Pasted image':note.split('\n')[0].slice(0,60)),url:clean,site,ago:0,saved:'Nov 3',src,a:url?['new']:[],m:[],rl:later?1:0,sum:url?'Summary will appear here once the page has been read.':'',note,
      cv:imgs[0]?imgs[0].cv:url?{t:'grad',ar:1.5,c:['#2a3b44','#0e171a','#ff5f93']}:null,d:{read:'4 min',words:'—',author:'—',body:GENERIC_BODY,desc:'',ocr:'',colors:['#2a3b44','#ff5f93'],dims:'—',bytes:'—',sha:'—',mirrored:false},hl:[]};
    S.items.unshift(it);
    if(location.hash==='#home')refreshHome();
    toast(`Ready: ${it.title}`);
  },2600);
}
ACT['sv-url']=()=>{const q=$('#q');const v=q.value.trim();q.value='';S.ui.q='';$('.savebar').hidden=true;saveNew({url:v,note:'',imgs:[],later:false,src:'app'})};

/* ---- needs attention ---- */
V.attention=()=>`<div class="page narrow">${head('Needs attention',{back:'home',label:'Library'})}
 <div class="pagehead"><div class="grow"><h1 class="h1">Needs attention</h1><p class="lead">A save never fails. These could not be enriched after three tries over an hour. The link is kept, and so is anything you wrote.</p></div></div>
 ${S.failed.length?`<div class="panel"><div class="rows">${S.failed.map(f=>`<div><div class="grow"><div class="t">${esc(f.url)}</div><div class="s">${esc(f.err)} · ${f.tries} of 3 tries · ${esc(f.at)} · via ${SOURCES[f.src].n}</div></div><button class="btn sm" data-act="f-retry" data-id="${f.id}">${ico('refresh-cw')}Retry</button><button class="btn sm ghost" data-act="f-drop" data-id="${f.id}">Dismiss</button></div>`).join('')}</div></div>`:`<div class="empty"><h3>Nothing needs attention.</h3><p>Failed saves will appear here with a one-tap retry.</p></div>`}</div>`;
ACT['f-retry']=el=>{S.failed=S.failed.filter(f=>f.id!==el.dataset.id);toast('Retrying. It will leave the queue in a minute or so.');route()};
ACT['f-drop']=el=>{S.failed=S.failed.filter(f=>f.id!==el.dataset.id);route()};

/* ---- first run ---- */
V.firstrun=()=>`<div class="page"><div class="firstrun">
 <div><div class="row" style="margin-bottom:22px">${MARK}<b style="font-size:20px">Harbor</b></div><h1 class="h1">Bring something in.</h1><p class="lead" style="margin-top:12px">Your library is empty. Start with the old one, or save a single link and watch it organise itself.</p></div>
 <div class="paths"><button class="path" data-act="go" data-to="import">${ico('upload')}<span class="h3">Import from Raindrop</span><span class="muted">A CSV export or a Netscape bookmarks file. Collections become Spaces.</span></button>
 <button class="path" data-act="compose">${ico('link')}<span class="h3">Paste a first link</span><span class="muted">Saved in one motion. Kind, tags and summary appear a moment later.</span></button></div>
 <p class="hint">Every other door (Telegram, the share sheet, the quick-save page) is set up in Settings, Sources.</p></div></div>`;
