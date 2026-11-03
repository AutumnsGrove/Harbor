/* Item page (one layout per kind), reader with highlights, archive viewer. */
const NOTE_PREVIEW={};
const hex=n=>HL[n]||HL.amber;

function mediaBlock(it){
  if(!it.cv)return '';
  const tall=['book','movie','paper'].includes(it.kind)?' tall':'';
  const play=it.kind==='video'?`<div class="play" style="position:absolute;inset:0;display:grid;place-items:center"><span style="width:68px;height:68px;border-radius:50%;background:rgba(6,12,14,.6);backdrop-filter:blur(8px);display:grid;place-items:center">${ico('play')}</span></div><span class="badge" style="right:12px;bottom:12px">${it.d.dur}</span>`:'';
  return `<div class="frame${tall}">${cover(it)}${play}${it.dead?`<span class="badge warn" style="left:12px;bottom:12px">${ico('unlink')}Link dead</span>`:''}</div>`;
}
function facts(rows){return `<dl class="facts">${rows.filter(r=>r[1]).map(([k,v])=>`<div><dt>${k}</dt><dd>${v}</dd></div>`).join('')}</dl>`}
function kindPanel(it){
  const d=it.d||{};
  switch(it.kind){
   case 'article':return `<div class="sec"><div class="eyebrow">Reading</div>${facts([['Length',d.read],['Words',d.words],['Author',esc(d.author)],['Highlights',(it.hl||[]).length?String((it.hl||[]).length):'None yet']])}</div>`;
   case 'recipe':return `<div class="sec"><div class="eyebrow">Recipe</div>${facts([['Time',d.time],['Makes',d.yield]])}<div class="cols2"><div><div class="lab" style="margin-bottom:6px">Ingredients</div><ul class="ing">${d.ing.map(([q,n])=>`<li><b class="num">${esc(q)}</b>${esc(n)}</li>`).join('')}</ul></div><div><div class="lab" style="margin-bottom:10px">Method</div><ol class="steps">${d.steps.map(s=>`<li>${esc(s)}</li>`).join('')}</ol></div></div></div>`;
   case 'video':return `<div class="sec"><div class="eyebrow">Video</div>${facts([['Length',d.dur],['Channel',esc(d.channel)],['Transcript',d.ts==='ok'?'<span class="okc">Indexed</span>':'<span class="ambc">Unavailable</span>']])}
     ${d.chap.length?`<div><div class="lab" style="margin-bottom:4px">Chapters</div><ul class="chap">${d.chap.map(([t,n])=>`<li data-act="toast" data-m="Seeks the original video to ${t}"><time>${t}</time>${esc(n)}</li>`).join('')}</ul></div>`:''}</div>
     <div class="sec"><div class="eyebrow">Transcript</div>${d.ts==='ok'?`<div class="tsl">${d.lines.map(([t,l])=>`<p><time>${t}</time><span>${esc(l)}</span></p>`).join('')}</div><p class="hint">Fetched from the potato. Searchable from Home.</p>`:`<div class="callout"><span>No captions were available. The item is saved and searchable by title, summary and your note.</span></div>`}</div>`;
   case 'product':return `<div class="sec"><div class="eyebrow">Product</div><div class="row wrap" style="gap:28px;align-items:flex-end"><div><div class="h1 num" style="font-size:34px">${esc(d.price)}</div><div class="${d.avail==='In stock'?'okc':'ambc'}" style="margin-top:2px">${esc(d.avail)} · ${esc(d.brand)}</div></div><div><div class="lab" style="margin-bottom:4px">Price since saved</div>${spark(d.hist,170,40,'var(--ok)')}</div></div><dl class="specs">${d.specs.map(([k,v])=>`<dt>${k}</dt><dd>${esc(v)}</dd>`).join('')}</dl></div>`;
   case 'book':return `<div class="sec"><div class="eyebrow">Book</div>${facts([['Author',esc(d.author)],['Year',d.year],['Pages',d.pages],['Publisher',esc(d.publisher)],['ISBN',`<span class="num">${d.isbn}</span>`]])}</div>`;
   case 'tweet':return `<div class="sec"><div class="eyebrow">Post</div><blockquote style="margin:0;background:var(--surf);border-radius:var(--r-l);padding:26px 28px"><p style="font-size:26px;line-height:1.3;font-weight:500;letter-spacing:-.01em;text-wrap:balance">${esc(d.text)}</p><p class="muted" style="margin-top:16px">${esc(d.name)} <span class="dim">${esc(d.handle)}</span></p></blockquote></div>`;
   case 'note':return '';
   case 'image':return `<div class="sec"><div class="eyebrow">Image</div><p style="font-size:var(--t-l)">${esc(d.desc)||'<span class="dim">No description yet.</span>'}</p>${facts([['Size',d.dims],['File',d.bytes]])}
     ${d.ocr?`<div><div class="lab" style="margin-bottom:6px">Text in the image (OCR)</div><div class="copy"><span style="white-space:normal">${esc(d.ocr)}</span><button class="ibtn plain" data-act="toast" data-m="Copied the text" aria-label="Copy text">${ico('copy')}</button></div></div>`:''}
     <div><div class="lab" style="margin-bottom:8px">Colours</div><div class="row">${d.colors.map(c=>`<span title="${c}" style="width:34px;height:34px;border-radius:50%;background:${c};box-shadow:inset 0 0 0 1px rgba(255,255,255,.12)"></span>`).join('')}</div></div></div>`;
   case 'paper':return `<div class="sec"><div class="eyebrow">Paper</div>${facts([['Authors',esc(d.authors)],['Source',esc(d.venue)],['Pages',d.pages],['Text',d.text==='ok'?'<span class="okc">Text layer indexed</span>':'<span class="ambc">Scan, OCR on 10 pages</span>']])}<div><div class="lab" style="margin-bottom:6px">Abstract</div><p class="muted" style="line-height:1.6">${esc(d.abstract)}</p></div><p class="hint">Papers are view-only in v1. Highlights work on articles.</p></div>`;
   case 'podcast':return `<div class="sec"><div class="eyebrow">Episode</div>${facts([['Show',esc(d.show)],['Episode',d.ep],['Length',d.dur],['Transcript',d.ts==='published'?'<span class="okc">Published, indexed</span>':'<span class="ambc">None</span>']])}<p class="muted" style="line-height:1.6">${esc(d.notes)}</p></div>`;
   case 'place':return `<div class="sec"><div class="eyebrow">Place</div>${facts([['Address',esc(d.addr)],['Hours',esc(d.hours)],['Rating',d.rating+' of 5'],['Phone',`<span class="num">${esc(d.phone)}</span>`]])}</div>`;
   case 'movie':return `<div class="sec"><div class="eyebrow">Film</div>${facts([['Year',d.year],['Director',esc(d.dir)],['Runtime',d.runtime],['Cast',esc(d.cast)],['Watch',esc(d.where)]])}</div>`;
  }return '';
}
function tagRow(it){
  return `<div class="tagrow" id="tagrow">${allTags(it).map(t=>`<span class="tag ${t.m?'m':''}" title="${t.m?'Your tag':'Added by AI'}"><i></i><span data-act="tag-go" data-t="${esc(t.n)}" role="button" tabindex="0" style="cursor:pointer">${esc(t.n)}</span><button data-act="tag-rm" data-t="${esc(t.n)}" data-m="${t.m}" aria-label="Remove tag ${esc(t.n)}" style="all:unset;cursor:pointer;color:var(--dim);display:grid;margin-left:2px">${ico('x')}</button></span>`).join('')}<input class="in" id="tag-in" list="vocab" placeholder="Add tag" aria-label="Add tag" autocomplete="off"><datalist id="vocab">${Object.keys(vocab()).map(t=>`<option value="${esc(t)}">`).join('')}</datalist></div>
  <p class="hint"><span class="dim">○</span> added by AI &nbsp; <span class="acc">●</span> yours. Spellings you merged in the tag manager map back automatically.</p>`;
}
function noteBox(it){
  const pv=NOTE_PREVIEW[it.id]??!!it.note;
  return `<div class="notebox" id="notebox"><div class="seg" role="group" aria-label="Note mode"><button data-act="note-mode" data-v="0" aria-pressed="${!pv}">Write</button><button data-act="note-mode" data-v="1" aria-pressed="${!!pv}">Preview</button></div>
  ${pv?`<div class="md">${it.note?md(it.note):'<span class="dim">Nothing yet.</span>'}</div>`:`<textarea class="ta" id="note-ta" data-edit="note" placeholder="Your own words. Markdown works." aria-label="Note">${esc(it.note)}</textarea>`}</div>`;
}
function hlSection(it){
  if(it.kind!=='article'||!(it.hl||[]).length)return '';
  const ok=it.hl.filter(h=>!h.detached),det=it.hl.filter(h=>h.detached);
  return `<div class="sec"><div class="eyebrow">Highlights · ${ok.length}</div>${ok.map(h=>`<div class="hlcard" style="--hc:${hex(h.color)}"><q>${esc(h.quote)}</q>${h.note?`<p>${esc(h.note)}</p>`:''}</div>`).join('')}
  ${det.length?`<div class="lab" style="margin-top:6px">Couldn’t re-anchor after the last extraction</div>${det.map(h=>`<div class="hlcard det" style="--hc:${hex(h.color)}"><q>${esc(h.quote)}</q></div>`).join('')}`:''}<div class="actions"><button class="btn sm" data-act="go" data-to="reader-${it.id}">Open in reader</button></div></div>`;
}
V.item=id=>{
  const it=find(id);if(!it)return `<div class="page narrow">${head('Not found')}<div class="empty"><h3>That item is not in the library.</h3><p>It may be in Recently deleted.</p></div></div>`;
  const K=KINDS[it.kind],media=mediaBlock(it),hasUrl=!!it.url&&it.kind!=='note';
  const rel=related(it);
  const more=`<button class="ibtn" data-act="item-more" data-id="${it.id}" aria-label="More actions">${ico('ellipsis')}</button>`;
  return `<div class="page ${media?'':'mid'}">${head(K.n,{back:'home',label:'Library',right:more})}
  <div class="${media?'item-grid':''}">
   ${media?`<div class="item-media">${media}</div>`:''}
   <div class="item-body">
    <div><div class="eyebrow" style="margin-bottom:10px">${K.n}${it.rl?' · <span class="ambc">Read later</span>':''}</div>
     <h1 class="item-title" contenteditable="plaintext-only" spellcheck="false" data-edit="title" aria-label="Title">${esc(it.title)}</h1>
     <div class="metaline"><button class="pill" data-act="kind-pick" data-id="${it.id}" title="Change kind">${ico(K.i)}${K.n}${ico('chevron-down')}</button><span>${esc(it.site)}</span><i class="dot"></i><span>Saved ${esc(it.saved)}</span><i class="dot"></i><span>${ico(SOURCES[it.src].i)} via ${SOURCES[it.src].n}</span>${it.dead?`<span class="pill amb">${ico('unlink')}Link dead, archive kept</span>`:''}</div></div>
    <div class="actions">${it.kind==='article'?`<button class="btn pri" data-act="go" data-to="reader-${it.id}">${ico('book-open')}Read</button>`:''}${hasUrl?`<button class="btn ${it.kind==='article'?'':'pri'}" data-act="toast" data-m="Opens ${esc(it.url)} in a new tab">${ico('external-link')}Open original</button><button class="btn" data-act="go" data-to="archive-${it.id}">${ico('archive')}Archive</button>`:''}<button class="btn ${it.rl?'amb':''}" data-act="rl" data-id="${it.id}" aria-pressed="${!!it.rl}">${ico('clock')}${it.rl?'In Read later':'Read later'}</button><button class="btn ghost" data-act="item-space" data-id="${it.id}">${ico('layers')}Space</button>${hasUrl?`<button class="btn ghost" data-act="toast" data-m="Refreshing metadata, screenshot and archive">${ico('refresh-cw')}Refresh</button>`:''}</div>
    ${it.sum?`<div class="sumbox"><div class="eyebrow">Summary <span class="dim" style="letter-spacing:0;text-transform:none">· written by AI</span></div><p>${esc(it.sum)}</p></div>`:''}
    ${kindPanel(it)}
    <div class="sec"><div class="eyebrow">${it.kind==='note'?'Note':'Your note'}</div>${noteBox(it)}</div>
    <div class="sec"><div class="eyebrow">Tags</div>${tagRow(it)}</div>
    ${hlSection(it)}
    ${rel.length?`<div class="sec"><div class="eyebrow">Related</div><div class="relrow">${rel.map(o=>`<a class="rel" data-act="go" data-to="item-${o.id}" role="link" tabindex="0"><div class="m">${o.cv?cover(o):`<span class="dim">${ico(KINDS[o.kind].i)}</span>`}</div><p>${esc(o.title)}</p></a>`).join('')}</div></div>`:''}
    <div class="sec"><div class="eyebrow">Details</div><dl class="prov">
      ${it.url?`<dt>Address</dt><dd>${esc(it.url)}</dd>`:''}<dt>Saved</dt><dd>${esc(it.saved)}, via ${SOURCES[it.src].n}</dd>
      ${it.url?`<dt>Archive</dt><dd>${it.dead?'Last good copy kept':'HTML and screenshot'} · <button class="lnk" data-act="toast" data-m="PDF generated and stored in R2" style="all:unset;cursor:pointer;color:var(--acc)">Make PDF</button></dd><dt>Link check</dt><dd class="${it.dead?'ambc':'okc'}">${it.dead?'Gone (404) on Nov 3':'Fine on Nov 3'}</dd>`:''}
      <dt>Enriched</dt><dd>Prompt v3 · kind by Jev, text by DeepSeek Flash</dd>${it.kind==='image'?`<dt>Mirror</dt><dd class="${it.d.mirrored?'okc':'ambc'}">${it.d.mirrored?'Copied to R2':'Waiting to copy to R2'}</dd><dt>SHA-256</dt><dd>${esc(it.d.sha)}</dd>`:''}
    </dl></div>
   </div></div></div>`;
};
/* item edits */
document.addEventListener('focusout',e=>{const t=e.target;if(t.dataset?.edit==='title'){const it=find(S.route.arg);if(it){const v=t.textContent.trim()||it.title;if(v!==it.title){it.title=v;toast('Title updated')}t.textContent=it.title}}});
document.addEventListener('keydown',e=>{
  if(e.target.dataset?.edit==='title'&&e.key==='Enter'){e.preventDefault();e.target.blur()}
  if(e.target.id==='tag-in'&&e.key==='Enter'){const it=find(S.route.arg),t=canon(e.target.value);if(!t)return;if(!tagNames(it).includes(t)){it.m.push(t);toast(`Added “${t}”`)}route();setTimeout(()=>$('#tag-in')?.focus(),30)}
});
document.addEventListener('input',e=>{if(e.target.dataset?.edit==='note'){const it=find(S.route.arg);if(it)it.note=e.target.value}});
ACT['note-mode']=el=>{const it=find(S.route.arg);NOTE_PREVIEW[it.id]=el.dataset.v==='1';$('#notebox').outerHTML=noteBox(it);lucide?.createIcons({root:$('#notebox')})};
ACT['tag-go']=el=>{S.ui.tag=el.dataset.t;S.ui.q='';location.hash='#home';if(location.hash==='#home')route()};
ACT['tag-rm']=el=>{const it=find(S.route.arg),t=el.dataset.t;it.m=it.m.filter(x=>x!==t);it.a=it.a.filter(x=>x!==t);toast(`Removed “${t}”`,()=>{(el.dataset.m==='1'?it.m:it.a).push(t);route()});route()};
ACT.rl=el=>{const it=find(el.dataset.id);it.rl=it.rl?0:1;toast(it.rl?'Added to Read later':'Removed from Read later');route()};
ACT.toast=el=>toast(el.dataset.m);
ACT['kind-pick']=el=>{const it=find(el.dataset.id);openMenu(el,Object.entries(KINDS).map(([k,v])=>({label:v.n,icon:v.i,v:k,on:it.kind===k})),i=>{it.kind=i.v;toast(`Changed kind to ${KINDS[i.v].n}`);route()})};
ACT['item-space']=el=>{const it=find(el.dataset.id),man=S.spaces.filter(s=>s.t==='manual');openMenu(el,man.map(s=>({label:s.n,v:s.id,on:s.id==='later'?!!it.rl:(s.ids||[]).includes(it.id)})),i=>{if(i.v==='later')it.rl=it.rl?0:1;else{const sp=S.spaces.find(s=>s.id===i.v);sp.ids=sp.ids||[];const k=sp.ids.indexOf(it.id);k<0?sp.ids.push(it.id):sp.ids.splice(k,1)}toast('Spaces updated');route()})};
ACT['item-more']=el=>{const it=find(el.dataset.id);openMenu(el,[{label:'Copy link',icon:'link',v:'copy'},{label:'Refresh',icon:'refresh-cw',v:'refresh'},'-',{label:'Delete',icon:'trash-2',v:'del'}],i=>{
  if(i.v==='copy')toast('Link copied');if(i.v==='refresh')toast('Refreshing metadata, screenshot and archive');
  if(i.v==='del')confirmBox('Delete this item?','It moves to Recently deleted in Settings and is purged after 30 days.','Delete',()=>{trash([it.id]);location.hash='#home';toast('Moved to Recently deleted',()=>restore([it.id]))})})};

/* ---- reader ---- */
function articleHTML(it){
  const hls=(it.hl||[]).filter(h=>!h.detached);
  return (it.d.body||GENERIC_BODY).map(([t,x])=>{
    let h=esc(x);
    if(t==='p')hls.forEach(hl=>{const q=esc(hl.quote);if(h.includes(q)&&!h.includes(`data-h="${hl.id}"`))h=h.replace(q,`<mark data-h="${hl.id}" style="--hc:${hex(hl.color)}">${q}</mark>`)});
    return t==='h'?`<h2>${h}</h2>`:`<p>${h}</p>`}).join('');
}
function sideHTML(it){
  const hl=it.hl||[],ok=hl.filter(h=>!h.detached),det=hl.filter(h=>h.detached);
  return `<div class="row"><div class="eyebrow" style="flex:1">Highlights · ${ok.length}</div></div>
  ${ok.length?'':`<p class="muted">Select any text in the article to highlight it. Pick one of four colours, then add a note.</p>`}
  ${ok.map(h=>`<div class="hlcard" style="--hc:${hex(h.color)}" id="hc-${h.id}"><q>${esc(h.quote)}</q><div class="swatches" style="margin-top:12px">${HL_NAMES.map(n=>`<button style="--c:${HL[n]}" data-act="hl-color" data-h="${h.id}" data-c="${n}" aria-pressed="${h.color===n}" aria-label="${n}"></button>`).join('')}<span class="sp"></span><button class="ibtn plain" style="width:24px;height:24px;background:none" data-act="hl-del" data-h="${h.id}" aria-label="Delete highlight">${ico('trash-2')}</button></div><textarea class="ta" data-edit="hl" data-h="${h.id}" placeholder="Add a note to this highlight" style="min-height:62px;margin-top:10px;background:var(--surf2);padding:10px 12px;font-size:var(--t-m)" aria-label="Annotation">${esc(h.note)}</textarea></div>`).join('')}
  ${det.length?`<div class="eyebrow" style="margin-top:10px">Couldn’t re-anchor</div><p class="hint">The page text changed and these quotes no longer match. Harbor will not guess.</p>${det.map(h=>`<div class="hlcard det" style="--hc:${hex(h.color)}"><q>${esc(h.quote)}</q><div class="actions" style="margin-top:10px"><button class="btn sm ghost" data-act="hl-del" data-h="${h.id}">Dismiss</button></div></div>`).join('')}`:''}
  <p class="hint">Reader text version 3 · highlights stored as quote, prefix and suffix.</p>`;
}
V.reader=id=>{
  const it=find(id);if(!it||it.kind!=='article')return `<div class="page narrow">${head('Reader')}<div class="empty"><h3>The reader is for articles.</h3><p>Papers and archives are view-only in v1.</p></div></div>`;
  const tools=`<button class="btn sm ghost" data-act="rs" data-v="-1" aria-label="Smaller text">A−</button><button class="btn sm ghost" data-act="rs" data-v="1" aria-label="Larger text">A+</button><button class="btn sm" data-act="go" data-to="archive-${it.id}">${ico('archive')}Archive</button>`;
  return `<div class="rtool"><button class="ibtn" data-act="go" data-to="item-${it.id}" aria-label="Back to item">${ico('chevron-left')}</button><span class="muted" style="font-size:var(--t-s)">${esc(it.site)}</span><span class="sp"></span>${S.ui.phone?'':tools}${S.ui.phone?`<button class="ibtn" data-act="rs" data-v="1" aria-label="Larger text">${ico('a-large-small')}</button>`:''}</div>
  <div class="reader" style="--rs:${S.rs||19}px"><article class="article" id="art"><div class="eyebrow site">${esc(it.site)} · ${it.d.read}</div><h1>${esc(it.title)}</h1><p class="byline">${esc(it.d.author)} · saved ${esc(it.saved)}</p>${articleHTML(it)}</article><aside class="side" id="side">${sideHTML(it)}</aside></div>`;
};
ACT.rs=el=>{S.rs=Math.max(15,Math.min(26,(S.rs||19)+(+el.dataset.v)*2));$('.reader').style.setProperty('--rs',S.rs+'px')};
function reRead(){const it=find(S.route.arg),a=app(),st=a.scrollTop;route();a.scrollTop=st}
ACT['hl-color']=el=>{const it=find(S.route.arg),h=it.hl.find(x=>x.id===el.dataset.h);h.color=el.dataset.c;reRead()};
ACT['hl-del']=el=>{const it=find(S.route.arg);it.hl=it.hl.filter(x=>x.id!==el.dataset.h);toast('Highlight removed');reRead()};
document.addEventListener('input',e=>{if(e.target.dataset?.edit==='hl'){const it=find(S.route.arg),h=it.hl.find(x=>x.id===e.target.dataset.h);if(h)h.note=e.target.value}});
document.addEventListener('click',e=>{const m=e.target.closest('#art mark');if(m){const c=$('#hc-'+m.dataset.h);c&&(c.scrollIntoView({block:'center',behavior:'smooth'}),c.querySelector('textarea')?.focus())}});
/* select to highlight */
function hideTool(){$('#hltool')?.remove()}
document.addEventListener('mouseup',e=>{
  if(S.route?.name!=='reader'||!e.target.closest('#art'))return;
  setTimeout(()=>{
    const sel=getSelection();hideTool();
    if(!sel||sel.isCollapsed||!sel.toString().trim())return;
    const r=sel.getRangeAt(0),p=r.commonAncestorContainer.nodeType===1?r.commonAncestorContainer:r.commonAncestorContainer.parentElement;
    const para=p.closest('#art p');if(!para||p.closest('mark'))return;
    const a=app(),ar=a.getBoundingClientRect(),rr=r.getBoundingClientRect();
    const t=document.createElement('div');t.id='hltool';t.className='hltool';t.setAttribute('role','toolbar');t.setAttribute('aria-label','Highlight colours');
    t.style.left=Math.max(8,rr.left-ar.left+rr.width/2-80)+'px';t.style.top=(rr.top-ar.top+a.scrollTop-52)+'px';
    t.innerHTML=HL_NAMES.map(n=>`<button class="sw" style="--c:${HL[n]}" data-q="${esc(sel.toString().trim())}" data-c="${n}" aria-label="Highlight ${n}"></button>`).join('');
    t.addEventListener('mousedown',ev=>ev.preventDefault());
    t.addEventListener('click',ev=>{const b=ev.target.closest('.sw');if(!b)return;
      const it=find(S.route.arg),q=b.dataset.q;
      if(!para.textContent.includes(q)){toast('Select inside one paragraph');return}
      it.hl=it.hl||[];const h={id:'h'+Date.now(),color:b.dataset.c,quote:q,note:''};it.hl.push(h);getSelection().removeAllRanges();hideTool();
      reRead();setTimeout(()=>{const c=$('#hc-'+h.id);c&&(c.scrollIntoView({block:'center'}),c.querySelector('textarea')?.focus())},60);toast('Highlighted. Add a note on the right.');
    });
    a.appendChild(t);
  },10);
});
document.addEventListener('mousedown',e=>{if(!e.target.closest('#hltool'))hideTool()});

/* ---- archive viewer ---- */
const ARCHIVE_PAGE=it=>`<!doctype html><meta charset=utf-8><style>body{font:17px/1.65 Georgia,serif;color:#222;max-width:640px;margin:34px auto;padding:0 22px}h1{font:700 30px/1.2 system-ui,sans-serif}nav{font:13px system-ui,sans-serif;color:#667;border-bottom:1px solid #ddd;padding-bottom:10px;margin-bottom:26px}.ad{background:#eee;color:#889;font:12px system-ui;padding:30px;text-align:center;margin:22px 0}</style><nav>${esc(it.site)} / docs / ${esc(it.title.toLowerCase().replace(/\s+/g,'-'))}</nav><h1>${esc(it.title)}</h1>${(it.d.body||GENERIC_BODY).map(([t,x])=>t==='h'?`<h3>${esc(x)}</h3>`:`<p>${esc(x)}</p>`).join('')}<div class="ad">An advertisement the archive could not run</div><p><a href="https://example.com">A link. It does nothing here.</a></p>`;
const ARCH_TAB={};
V.archive=id=>{
  const it=find(id);if(!it||!it.url)return `<div class="page narrow">${head('Archive')}<div class="empty"><h3>No archive for this item.</h3></div></div>`;
  const tab=ARCH_TAB[id]||'page';
  const body=tab==='page'?`<div class="archive-frame"><iframe sandbox="" srcdoc="${esc(ARCHIVE_PAGE(it))}" title="Archived copy of ${esc(it.title)}"></iframe></div>`
   :tab==='shot'?`<div class="frame" style="border-radius:var(--r-l);overflow:hidden">${it.cv?cover(it):''}</div>`
   :`<div class="panel"><div class="empty" style="padding:30px 0"><h3>No PDF yet</h3><p>PDFs are made on demand so they do not use Browser Rendering time you do not need.</p><button class="btn pri" style="margin-top:14px" data-act="toast" data-m="PDF queued. It will appear here in about a minute.">${ico('file-down')}Make a PDF</button></div></div>`;
  return `<div class="page mid">${head('Archive',{back:'item-'+id,label:it.title})}
   <div class="pagehead"><div class="grow"><div class="eyebrow">${esc(it.site)}</div><h1 class="h1" style="font-size:30px;margin-top:6px">${esc(it.title)}</h1></div><div class="seg" role="group" aria-label="Archive format"><button data-act="arch-tab" data-t="page" aria-pressed="${tab==='page'}">Page</button><button data-act="arch-tab" data-t="shot" aria-pressed="${tab==='shot'}">Screenshot</button><button data-act="arch-tab" data-t="pdf" aria-pressed="${tab==='pdf'}">PDF</button></div></div>
   <div class="callout ok" style="margin-bottom:16px"><span><b>Sandboxed copy.</b> Scripts, forms and plugins are switched off. Read it in the reader unless you need the original layout.</span></div>
   ${body}
   <div class="row wrap" style="margin-top:18px"><span class="muted" style="font-size:var(--t-s)">Saved ${esc(it.saved)} · latest copy only · ${it.dead?'<span class="ambc">link is dead, this copy is all that is left</span>':'unchanged since the last check'}</span><span class="sp"></span>${it.kind==='article'?`<button class="btn sm" data-act="go" data-to="reader-${id}">${ico('book-open')}Open in reader</button>`:''}<button class="btn sm" data-act="toast" data-m="Downloading the single-file HTML">${ico('download')}Download HTML</button></div></div>`;
};
ACT['arch-tab']=el=>{ARCH_TAB[S.route.arg]=el.dataset.t;route()};
