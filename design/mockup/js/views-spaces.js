/* Spaces list, Space editor (rule builder), tag manager. */
const spaceItems=sp=>S.items.filter(i=>ruleTest(sp,i));
function mosaic(sp){const l=spaceItems(sp).filter(i=>i.cv).slice(0,3);return `<div class="mosaic">${[0,1,2].map(k=>`<div>${l[k]?cover(l[k]):''}</div>`).join('')}</div>`}
const TYPE_LABEL={auto:'Auto',saved:'Saved search',manual:'Hand-picked'};
V.spaces=()=>`<div class="page">${head('Spaces',{back:'home',label:'Library',right:`<button class="ibtn acc" data-act="go" data-to="space-new" aria-label="New Space">${ico('plus')}</button>`})}
 <div class="pagehead"><div class="grow"><h1 class="h1">Spaces</h1><p class="lead">Ways to browse by theme, never folders. Auto Spaces and saved searches update themselves. Hand-picked ones are lists you curate.</p></div><button class="btn pri" data-act="go" data-to="space-new">${ico('plus')}New Space</button></div>
 ${S.suggestion?`<div class="sugg" style="margin-bottom:26px">${ico('sparkles')}<div style="flex:1"><b>${esc(S.suggestion.name)}?</b> <span class="muted">${esc(S.suggestion.why)} You name it and choose to keep it.</span></div><button class="btn sm" data-act="sugg-open">Review</button><button class="btn sm ghost" data-act="sugg-no">Not now</button></div>`:''}
 <div class="spgrid">${S.spaces.map(s=>`<div class="spcard" data-act="open-space" data-id="${s.id}" role="link" tabindex="0" aria-label="${esc(s.n)}">${mosaic(s)}<div><div class="row"><span class="h3" style="flex:1">${s.id==='later'?ico('clock','ambc')+' ':''}${esc(s.n)}</span><span class="num muted">${s.lib}</span></div><p class="muted" style="font-size:var(--t-s);margin-top:3px">${esc(ruleText(s))}</p><div class="row" style="margin-top:10px"><span class="pill">${TYPE_LABEL[s.t]}</span>${s.imported?'<span class="pill">Raindrop</span>':''}</div></div></div>`).join('')}</div></div>`;
ACT['open-space']=el=>{S.ui.space=el.dataset.id;S.ui.q='';S.ui.kind='all';location.hash='#home'};

/* editor */
let ED=null;
const BLANK_RULE=()=>({tags_all:[],tags_any:[],kinds:[],sources:[],within:0,text:'',similar_to:''});
function startEditor(arg){
  if(arg==='new'){
    const pre=S.prefill&&S.suggestion;
    ED={id:null,n:pre?S.suggestion.name:'',t:pre?'auto':'auto',rule:Object.assign(BLANK_RULE(),pre?JSON.parse(JSON.stringify(S.suggestion.rule)):{}),fromSugg:!!pre};
  }else{const sp=S.spaces.find(s=>s.id===arg);ED={id:sp.id,n:sp.n,t:sp.t,rule:Object.assign(BLANK_RULE(),JSON.parse(JSON.stringify(sp.rule||{})))}}
  S.prefill=false;
}
const edSpace=()=>({id:ED.id||'tmp',n:ED.n,t:ED.t,rule:ED.rule,ids:[]});
function tokenField(k,label,ph){
  return `<div class="rule"><div class="lab">${label}</div><div class="tokens">${ED.rule[k].map(t=>`<span class="token">${esc(t)}<button data-act="r-rm" data-k="${k}" data-t="${esc(t)}" aria-label="Remove ${esc(t)}">${ico('x')}</button></span>`).join('')}<input class="in" data-rk="${k}" list="vocab" placeholder="${ph}" aria-label="${label}" style="width:150px;height:32px;font-size:var(--t-s);padding:0 12px;border-radius:var(--pill)"></div></div>`;
}
function editRules(){
  if(ED.t==='manual')return `<div class="callout" style="margin:8px 0"><span>Hand-picked Spaces have no rule. Add items from an item page, or use Select on Home and choose Add to Space.</span></div>`;
  const r=ED.rule,sim=r.similar_to&&find(r.similar_to);
  return `<datalist id="vocab">${Object.keys(vocab()).map(t=>`<option value="${esc(t)}">`).join('')}</datalist>
  ${tokenField('tags_all','Tagged with all of','Add tag')}${tokenField('tags_any','Tagged with any of','Add tag')}
  <div class="rule"><div class="lab">Kind</div><div class="tokens">${Object.keys(KINDS).map(k=>`<button class="chip sm ghost" data-act="r-toggle" data-k="kinds" data-v="${k}" aria-pressed="${r.kinds.includes(k)}">${KINDS[k].n}</button>`).join('')}</div></div>
  <div class="rule"><div class="lab">Saved from</div><div class="tokens">${Object.entries(SOURCES).map(([k,v])=>`<button class="chip sm ghost" data-act="r-toggle" data-k="sources" data-v="${k}" aria-pressed="${r.sources.includes(k)}">${v.n}</button>`).join('')}</div></div>
  <div class="rule"><div class="lab">Saved within</div><div class="tokens">${[[0,'Any time'],[7,'7 days'],[14,'14 days'],[30,'30 days'],[90,'90 days']].map(([v,l])=>`<button class="chip sm ghost" data-act="r-within" data-v="${v}" aria-pressed="${r.within===v}">${l}</button>`).join('')}</div></div>
  <div class="rule"><div class="lab">Mentions</div><input class="in" id="r-text" placeholder="A word or phrase" value="${esc(r.text)}" style="height:38px;font-size:var(--t-m)"></div>
  <div class="rule"><div class="lab">Similar to</div><div class="tokens">${sim?`<span class="token">${esc(sim.title)}<button data-act="r-sim-clear" aria-label="Clear">${ico('x')}</button></span>`:`<button class="btn sm" data-act="r-sim">${ico('sparkles')}Pick an item</button>`}<span class="hint">Uses the same index as Related.</span></div></div>`;
}
function editPreview(){
  const sp=edSpace(),list=ED.t==='manual'?[]:spaceItems(sp);
  return `<div class="row" style="margin-bottom:12px"><div class="eyebrow" style="flex:1">Preview</div><span class="num muted">${ED.t==='manual'?'Empty to start':list.length+' match'+(list.length===1?'':'es')+' in this sample'}</span></div>
  ${list.length?`<div class="mini">${list.slice(0,12).map(i=>`<div title="${esc(i.title)}">${i.cv?cover(i):`<div style="padding:10px;font-size:11px;color:var(--mute)">${esc(i.title)}</div>`}</div>`).join('')}</div>`:`<p class="muted">${ED.t==='manual'?'Nothing yet.':'No items match this rule yet.'}</p>`}`;
}
V['space-edit']=arg=>{
  if(!ED)startEditor(arg);
  const isNew=!ED.id;
  return `<div class="page mid">${head(isNew?'New Space':'Edit Space',{back:'spaces',label:'Spaces'})}
  <div class="pagehead"><div class="grow"><h1 class="h1">${isNew?'New Space':'Edit '+esc(ED.n)}</h1>${ED.fromSugg?`<p class="lead">Harbor spotted this pattern. Rename it, adjust the rule, or discard it.</p>`:''}</div></div>
  <div class="stack" style="gap:28px">
   <div class="field"><label for="sp-name">Name</label><input class="in" id="sp-name" placeholder="e.g. Things to try this month" value="${esc(ED.n)}"></div>
   <div class="field"><label>Type</label><div class="seg" role="group" aria-label="Space type" style="width:max-content;max-width:100%"><button data-act="ed-type" data-v="auto" aria-pressed="${ED.t==='auto'}">Auto</button><button data-act="ed-type" data-v="saved" aria-pressed="${ED.t==='saved'}">Saved search</button><button data-act="ed-type" data-v="manual" aria-pressed="${ED.t==='manual'}">Hand-picked</button></div><p class="hint">${{auto:'Follows its rule and grows by itself. Harbor can suggest these, but never creates or renames one without you.',saved:'The same rule engine, kept as a search you named.',manual:'A list you curate. Read later and imported collections are hand-picked.'}[ED.t]}</p></div>
   <div class="field"><label>Rule <span class="dim">(every row must match)</span></label><div id="ed-rules" class="panel" style="padding:8px 22px">${editRules()}</div></div>
   <div class="panel" id="ed-preview">${editPreview()}</div>
   <div class="row wrap">${isNew?'':`<button class="btn danger ghost" data-act="ed-del">${ico('trash-2')}Delete Space</button>`}<span class="sp"></span><button class="btn ghost" data-act="go" data-to="spaces">Cancel</button><button class="btn pri" data-act="ed-save">${isNew?'Create Space':'Save'}</button></div>
  </div></div>`;
};
function refreshEd(){$('#ed-rules').innerHTML=editRules();$('#ed-preview').innerHTML=editPreview();lucide?.createIcons({root:app()})}
document.addEventListener('input',e=>{if(!ED)return;if(e.target.id==='sp-name')ED.n=e.target.value;if(e.target.id==='r-text'){ED.rule.text=e.target.value;$('#ed-preview').innerHTML=editPreview();lucide?.createIcons({root:$('#ed-preview')})}});
document.addEventListener('keydown',e=>{const k=e.target.dataset?.rk;if(k&&e.key==='Enter'){const t=canon(e.target.value);if(t&&!ED.rule[k].includes(t))ED.rule[k].push(t);refreshEd();setTimeout(()=>$(`[data-rk="${k}"]`)?.focus(),20)}});
ACT['ed-type']=el=>{ED.t=el.dataset.v;route(true)};
ACT['r-rm']=el=>{ED.rule[el.dataset.k]=ED.rule[el.dataset.k].filter(t=>t!==el.dataset.t);refreshEd()};
ACT['r-toggle']=el=>{const a=ED.rule[el.dataset.k],v=el.dataset.v,i=a.indexOf(v);i<0?a.push(v):a.splice(i,1);refreshEd()};
ACT['r-within']=el=>{ED.rule.within=+el.dataset.v;refreshEd()};
ACT['r-sim']=el=>openMenu(el,S.items.slice(0,12).map(i=>({label:i.title.slice(0,38),v:i.id})),i=>{ED.rule.similar_to=i.v;refreshEd()});
ACT['r-sim-clear']=()=>{ED.rule.similar_to='';refreshEd()};
ACT['ed-save']=()=>{
  const n=ED.n.trim();if(!n){toast('Give the Space a name first.');$('#sp-name').focus();return}
  const rule=ED.t==='manual'?undefined:JSON.parse(JSON.stringify(ED.rule));
  if(rule){if(!rule.within)delete rule.within;if(!rule.text)delete rule.text;if(!rule.similar_to)delete rule.similar_to;['tags_all','tags_any','kinds','sources'].forEach(k=>{if(!rule[k].length)delete rule[k]})}
  if(ED.id){const sp=S.spaces.find(s=>s.id===ED.id);Object.assign(sp,{n,t:ED.t,rule})}
  else{S.spaces.push({id:'s'+Date.now(),n,t:ED.t,rule,ids:[],lib:0});if(ED.fromSugg)S.suggestion=null}
  const sp=ED.id?S.spaces.find(s=>s.id===ED.id):S.spaces[S.spaces.length-1];
  if(!ED.id)sp.lib=ED.t==='manual'?0:spaceItems(sp).length;
  ED=null;toast(`Saved “${n}”`);location.hash='#spaces';
};
ACT['ed-del']=()=>confirmBox('Delete this Space?','The items in it are not deleted. Only the Space goes.','Delete Space',()=>{S.spaces=S.spaces.filter(s=>s.id!==ED.id);if(S.ui.space===ED.id)S.ui.space=null;ED=null;toast('Space deleted');location.hash='#spaces'});

/* tag manager */
const TAGUI={sort:'count',q:'',sel:new Set()};
function tagRows(){
  const v=vocab(),al={};Object.entries(S.tagAlias).forEach(([a,c])=>(al[c]=al[c]||[]).push(a));
  let rows=Object.keys(v).map(t=>({t,n:v[t],ai:S.items.filter(i=>i.a.includes(t)).length,mn:S.items.filter(i=>i.m.includes(t)).length,al:al[t]||[]}));
  const q=TAGUI.q.trim().toLowerCase();if(q)rows=rows.filter(r=>r.t.includes(q)||r.al.some(a=>a.toLowerCase().includes(q)));
  rows.sort((a,b)=>TAGUI.sort==='count'?b.n-a.n||a.t.localeCompare(b.t):a.t.localeCompare(b.t));return rows;
}
function tagTable(){
  const rows=tagRows(),ph=S.ui.phone;
  return `<table class="tbl"><thead><tr><th style="width:34px"></th><th>Tag</th><th class="r">Items</th><th class="hidephone">AI · yours</th><th class="hidephone">Also known as</th><th class="r"></th></tr></thead><tbody>${rows.map(r=>`<tr><td><button class="checkbox" role="checkbox" aria-checked="${TAGUI.sel.has(r.t)}" aria-label="Select ${esc(r.t)}" data-act="tg-sel" data-t="${esc(r.t)}">${ico('check')}</button></td><td><b style="font-weight:500">${esc(r.t)}</b></td><td class="r num">${r.n}</td><td class="hidephone muted num">${r.ai} · ${r.mn}</td><td class="hidephone dim">${r.al.map(esc).join(', ')||'–'}</td><td class="r"><button class="ibtn plain" data-act="tg-menu" data-t="${esc(r.t)}" aria-label="Actions for ${esc(r.t)}">${ico('ellipsis')}</button></td></tr>`).join('')}</tbody></table>${rows.length?'':'<div class="empty"><h3>No tags match.</h3></div>'}`;
}
V.tags=()=>`<div class="page narrow">${head('Tags',{back:'prefs',label:'Settings'})}
 <div class="pagehead"><div class="grow"><h1 class="h1">Tags</h1><p class="lead">Rename, merge or delete. Merging keeps the old spelling as an alias, so a re-import or a later AI tag lands on the one you chose.</p></div></div>
 <div class="row wrap" style="margin-bottom:18px"><div class="searchbox" style="max-width:340px"><label>${ico('search')}<input id="tag-q" placeholder="Find a tag" aria-label="Find a tag" value="${esc(TAGUI.q)}"></label></div><div class="seg" role="group" aria-label="Sort"><button data-act="tg-sort" data-v="count" aria-pressed="${TAGUI.sort==='count'}">Most used</button><button data-act="tg-sort" data-v="name" aria-pressed="${TAGUI.sort==='name'}">A to Z</button></div><span class="sp"></span><span id="tg-merge">${tgMergeBtn()}</span></div>
 <div class="panel" id="tg-table">${tagTable()}</div></div>`;
const tgMergeBtn=()=>TAGUI.sel.size>=2?`<button class="btn pri" data-act="tg-merge">${ico('merge')}Merge ${TAGUI.sel.size} tags</button>`:`<span class="hint">Select two or more to merge</span>`;
function tgRefresh(){$('#tg-table').innerHTML=tagTable();$('#tg-merge').innerHTML=tgMergeBtn();lucide?.createIcons({root:app()})}
document.addEventListener('input',e=>{if(e.target.id==='tag-q'){TAGUI.q=e.target.value;$('#tg-table').innerHTML=tagTable();lucide?.createIcons({root:$('#tg-table')})}});
ACT['tg-sort']=el=>{TAGUI.sort=el.dataset.v;route(true)};
ACT['tg-sel']=el=>{const t=el.dataset.t;TAGUI.sel.has(t)?TAGUI.sel.delete(t):TAGUI.sel.add(t);tgRefresh()};
ACT['tg-menu']=el=>{const t=el.dataset.t;openMenu(el,[{label:'Rename',icon:'pencil',v:'r'},{label:'Show items',icon:'search',v:'s'},'-',{label:'Delete tag',icon:'trash-2',v:'d'}],i=>{
  if(i.v==='s'){S.ui.tag=t;S.ui.q='';S.ui.kind='all';S.ui.space=null;location.hash='#home'}
  if(i.v==='r')modal(`<h2 class="h2">Rename “${esc(t)}”</h2><p class="muted">If the new name already exists, the two tags merge.</p><div class="field" style="margin-top:14px"><input class="in" id="tg-new" value="${esc(t)}" autofocus></div><div class="foot"><button class="btn ghost" data-act="close">Cancel</button><button class="btn pri" data-act="tg-rename" data-t="${esc(t)}">Rename</button></div>`);
  if(i.v==='d')confirmBox(`Delete “${t}”?`,`It is removed from ${count(vocab()[t],'item')}. The items stay.`,'Delete tag',()=>{S.items.forEach(x=>{x.a=x.a.filter(y=>y!==t);x.m=x.m.filter(y=>y!==t)});TAGUI.sel.delete(t);toast(`Deleted “${t}”`);tgRefresh()})})};
function mergeTags(srcs,target){
  S.items.forEach(it=>{const had=tagNames(it).some(t=>srcs.includes(t));srcs.forEach(s=>{if(s!==target){const man=it.m.includes(s);it.a=it.a.filter(x=>x!==s);it.m=it.m.filter(x=>x!==s);if(had&&!tagNames(it).includes(target))(man?it.m:it.a).push(target)}})});
  srcs.forEach(s=>{if(s!==target){S.tagAlias[s]=target;Object.keys(S.tagAlias).forEach(k=>{if(S.tagAlias[k]===s)S.tagAlias[k]=target})}});
}
ACT['tg-rename']=el=>{const old=el.dataset.t,nw=canon($('#tg-new').value);closeModal();if(!nw||nw===old)return;mergeTags([old],nw);S.tagAlias[old]=nw;TAGUI.sel.clear();toast(`Renamed to “${nw}”. “${old}” is now an alias.`);tgRefresh()};
ACT['tg-merge']=()=>{const ts=[...TAGUI.sel],v=vocab();ts.sort((a,b)=>v[b]-v[a]);
  modal(`<h2 class="h2">Merge ${ts.length} tags</h2><p class="muted">Pick the name to keep. The others become aliases.</p><div class="rows" style="margin-top:12px" role="radiogroup">${ts.map((t,i)=>`<label style="cursor:pointer"><input type="radio" name="keep" value="${esc(t)}" ${i?'':'checked'}> <span class="grow"><span class="t">${esc(t)}</span> <span class="dim">${v[t]} items</span></span></label>`).join('')}</div><div class="foot"><button class="btn ghost" data-act="close">Cancel</button><button class="btn pri" data-act="tg-merge-ok">Merge</button></div>`)};
ACT['tg-merge-ok']=()=>{const keep=$('input[name=keep]:checked').value,ts=[...TAGUI.sel];mergeTags(ts,keep);closeModal();TAGUI.sel.clear();toast(`Merged into “${keep}”. Old spellings are aliases now.`);tgRefresh()};
